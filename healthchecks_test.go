package healthchecks

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/etkecc/go-kit/httpclient"
	"github.com/google/uuid"
)

var (
	rid, _   = uuid.NewRandom()
	testUUID = "b6ff19a2-9497-457b-84d6-4f46aa3681a8"
	testRID  = rid.String()
)

func newC(t *testing.T, baseURL string) *Client {
	t.Helper()
	c := New(WithCheckUUID(testUUID))
	c.baseURL = baseURL
	c.rid = testRID

	return c
}

func newS(t *testing.T, expectedPath string, expectedRequestBody []byte, responseCode int) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualPath := r.URL.RequestURI()
		if actualPath != expectedPath {
			t.Error(expectedPath, "!=", actualPath)
		}
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("sent request body cannot be read")
		}
		if !bytes.Equal(expectedRequestBody, requestBody) {
			t.Log(string(expectedRequestBody), string(requestBody))
			t.Error("sent request body is not expected")
		}
		defer r.Body.Close()

		w.WriteHeader(responseCode)
	}))
}

func TestStart(t *testing.T) {
	s := newS(t, "/"+testUUID+"/start?rid="+testRID, nil, 200)
	c := newC(t, s.URL)

	c.Start()
}

func TestSuccess(t *testing.T) {
	s := newS(t, "/"+testUUID+"?rid="+testRID, nil, 200)
	c := newC(t, s.URL)

	c.Success()
}

func TestSuccessBody(t *testing.T) {
	s := newS(t, "/"+testUUID+"?rid="+testRID, []byte("hello"), 200)
	c := newC(t, s.URL)

	c.Success(strings.NewReader("hello"))
}

func TestSuccessError(t *testing.T) {
	s := newS(t, "/"+testUUID+"?rid="+testRID, []byte("hello"), 400)
	c := newC(t, s.URL)

	c.Success(strings.NewReader("hello"))
}

func TestFail(t *testing.T) {
	s := newS(t, "/"+testUUID+"/fail?rid="+testRID, nil, 200)
	c := newC(t, s.URL)

	c.Fail()
}

func TestLog(t *testing.T) {
	s := newS(t, "/"+testUUID+"/log?rid="+testRID, nil, 200)
	c := newC(t, s.URL)

	c.Log()
}

func TestExitStatus(t *testing.T) {
	s := newS(t, "/"+testUUID+"/5?rid="+testRID, nil, 200)
	c := newC(t, s.URL)

	c.ExitStatus(5)
}

// TestAuto performs tests with time.Sleep to ensure that requests were sent and ticker was closed after that
func TestAuto(t *testing.T) {
	s := newS(t, "/"+testUUID+"?rid="+testRID, nil, 200)
	c := newC(t, s.URL)

	go c.Auto(1 * time.Second)
	time.Sleep(1 * time.Second)
	c.done <- true
	time.Sleep(1 * time.Second)
}

func TestShutdown(t *testing.T) {
	c := newC(t, "")

	c.Shutdown()

	if !<-c.done {
		t.Fail()
	}
}

// capture is a concurrency-safe ErrLog sink: the call goroutines and the onAttempt bridge
// write to it from multiple goroutines, so a test can assert what was (and wasn't) logged.
type capture struct {
	mu   sync.Mutex
	errs []error
}

func (sink *capture) log(_ string, err error) {
	sink.mu.Lock()
	sink.errs = append(sink.errs, err)
	sink.mu.Unlock()
}

func (sink *capture) count() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return len(sink.errs)
}

func (sink *capture) has(target error) bool {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, err := range sink.errs {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// newCapC is newC plus a capturing ErrLog.
func newCapC(t *testing.T, baseURL string, sink *capture) *Client {
	t.Helper()
	c := New(WithCheckUUID(testUUID), WithErrLog(sink.log))
	c.baseURL = baseURL
	c.rid = testRID

	return c
}

// waitN drains n signals from ch, failing the test if they don't all arrive in time. The
// time.After is a failure bound, not a timing assertion, so it satisfies the no-time.Sleep rule.
func waitN(t *testing.T, ch <-chan struct{}, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for signal %d of %d", i+1, n)
		}
	}
}

// countingServer serves the given status codes in order (clamping to the last), counts each
// request, and signals reqCh after every one, so a test drains deterministically.
func countingServer(t *testing.T, reqCh chan<- struct{}, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(count.Add(1)) - 1
		_, _ = io.Copy(io.Discard, r.Body)
		code := codes[len(codes)-1]
		if i < len(codes) {
			code = codes[i]
		}
		w.WriteHeader(code)
		reqCh <- struct{}{}
	}))
	t.Cleanup(s.Close)

	return s, &count
}

// TestRetryOn5xx: an idempotent HEAD ping retries a 503 and succeeds on the 200, and the
// retry of a status (not a transport error) logs nothing.
func TestRetryOn5xx(t *testing.T) {
	reqCh := make(chan struct{}, 4)
	s, count := countingServer(t, reqCh, http.StatusServiceUnavailable, http.StatusOK)
	sink := &capture{}
	c := newCapC(t, s.URL, sink)

	c.Success()
	waitN(t, reqCh, 2)
	c.Shutdown()

	if got := count.Load(); got != 2 {
		t.Fatalf("expected 2 requests (503 then 200 retry), got %d", got)
	}
	if sink.count() != 0 {
		t.Fatalf("expected nothing logged on a 503->200 retry, got %v", sink.errs)
	}
}

// TestRetryNonIdempotentPOST: a POST body-ping retries too, because WithRetryNonIdempotent is
// wired and strings.Reader carries a GetBody to rewind.
func TestRetryNonIdempotentPOST(t *testing.T) {
	reqCh := make(chan struct{}, 4)
	s, count := countingServer(t, reqCh, http.StatusServiceUnavailable, http.StatusOK)
	sink := &capture{}
	c := newCapC(t, s.URL, sink)

	c.Success(strings.NewReader("x"))
	waitN(t, reqCh, 2)
	c.Shutdown()

	if got := count.Load(); got != 2 {
		t.Fatalf("expected POST to retry (WithRetryNonIdempotent wired), got %d requests", got)
	}
}

// TestNonReplayableBodyFailsLoud: a retryable POST whose body has no GetBody is refused before
// the first attempt (nothing sent) and the loud ErrNonReplayableBody reaches ErrLog.
func TestNonReplayableBodyFailsLoud(t *testing.T) {
	reqCh := make(chan struct{}, 1)
	s, count := countingServer(t, reqCh, http.StatusOK)
	sink := &capture{}
	c := newCapC(t, s.URL, sink)

	// A bare io.Reader wrapper: http.NewRequestWithContext can't synthesize a GetBody for it,
	// so the retry gate fires pre-first-attempt rather than replaying a consumed reader.
	c.Success(struct{ io.Reader }{strings.NewReader("x")})
	c.Shutdown()

	if got := count.Load(); got != 0 {
		t.Fatalf("expected 0 requests (gate fires before the first attempt), got %d", got)
	}
	if !sink.has(httpclient.ErrNonReplayableBody) {
		t.Fatalf("expected ErrNonReplayableBody logged, got %v", sink.errs)
	}
}

// TestSuccessFiresOnce: the success path sends exactly one request (idempotency invariant).
func TestSuccessFiresOnce(t *testing.T) {
	reqCh := make(chan struct{}, 4)
	s, count := countingServer(t, reqCh, http.StatusOK)
	c := newC(t, s.URL)

	c.Success()
	waitN(t, reqCh, 1)
	c.Shutdown()

	if got := count.Load(); got != 1 {
		t.Fatalf("success must fire exactly once, got %d", got)
	}
}

// TestShutdownCancelsInFlight: Shutdown returns while a ping's handler is still blocked, proving
// cancel() bounds the drain instead of waiting the retry budget out. The ordering is the risk.
func TestShutdownCancelsInFlight(t *testing.T) {
	release := make(chan struct{})
	reqCh := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reqCh <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	defer close(release)

	c := newC(t, s.URL)
	c.Success()
	<-reqCh // the ping is in-flight, server handler blocked

	done := make(chan struct{})
	go func() {
		c.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return with a ping in-flight: cancel/Wait ordering is broken")
	}
}

// TestClosedGuardRejectsAfterShutdown: once closed, Call spawns no goroutine and sends nothing.
func TestClosedGuardRejectsAfterShutdown(t *testing.T) {
	reqCh := make(chan struct{}, 1)
	s, count := countingServer(t, reqCh, http.StatusOK)
	c := newC(t, s.URL)

	c.Shutdown()
	c.Success()
	c.Success()

	if got := count.Load(); got != 0 {
		t.Fatalf("a closed client must not send, got %d requests", got)
	}
}

// TestConcurrentCallDuringShutdown races many Call()s against Shutdown(): the lock over the
// closed-check and wg.Add must keep any Add from landing while wg.Wait runs, which would panic
// the WaitGroup. Run under -race.
func TestConcurrentCallDuringShutdown(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	c := newC(t, s.URL)

	var callers sync.WaitGroup
	for range 50 {
		callers.Go(func() {
			c.Success()
		})
	}
	c.Shutdown()
	callers.Wait()
}

// TestSetEnabledConcurrent toggles SetEnabled while pinging: enabled shares the lock now, so
// -race must stay quiet.
func TestSetEnabledConcurrent(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	c := newC(t, s.URL)
	defer c.Shutdown()

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			c.SetEnabled(false)
			c.SetEnabled(true)
		}
	})
	wg.Go(func() {
		for range 50 {
			c.Success()
		}
	})
	wg.Wait()
}

// TestDoubleShutdown proves Shutdown is idempotent: the second call returns instead of blocking
// on the spent done buffer.
func TestDoubleShutdown(t *testing.T) {
	c := newC(t, "")

	c.Shutdown()
	c.Shutdown()
}

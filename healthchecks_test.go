package healthchecks

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

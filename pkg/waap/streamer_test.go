package waap

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/pianoyeg94/waap/pkg/cpu"
	"github.com/pianoyeg94/waap/pkg/strs"
)

func TestRequestStreamerStreamsRequestsToSink(t *testing.T) {
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	errorsSink := make(chan error, 2)
	processedStreamsSink := make(chan int, 1)
	requestsSink := make(chan string)
	defer func() {
		close(errorsSink)
		close(processedStreamsSink)
		close(requestsSink)
	}()

	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("Failed creating logger: %v", err)
	}

	corpusStreamer := newCorpusStreamer("./test-data", errorsSink, logger, ctx)
	corpusStreams, err := corpusStreamer.startStreamingCorpuses(&wg)
	if err != nil {
		t.Fatalf("Failed starting to stream corpus streams: %v", err)
	}

	requestStreamer := newRequestStreamer(
		corpusStreams,
		logger,
		processedStreamsSink,
		requestsSink,
		errorsSink,
		cpu.DoesCPUSuppportSIMD(),
		ctx,
	)
	requestStreamer.startStreamingRequestToSink(&wg)

Loop:
	for {
		select {
		case request := <-requestsSink:
			request = addHTTPVersionToRequestUrl(request)
			reader := bufio.NewReader(strings.NewReader(request))
			if _, err := http.ReadRequest(reader); err != nil {
				t.Errorf("requestStreamer produced and invalid HTTP request: err=%#v\nrequest=%#v", err, request)
			}
		case <-processedStreamsSink:
			break Loop
		case err := <-errorsSink:
			t.Fatalf("Received error from requestStreamer: %v", err)
		}
	}

	cancel()
	wg.Wait()
}

func addHTTPVersionToRequestUrl(request string) string {
	requestBytes := strs.StringToBytesZeroCopy(request)
	urlPlusRest := bytes.SplitN(requestBytes, crlf[:], 2)
	url, rest := urlPlusRest[0], urlPlusRest[1]

	var requestBuilder bytes.Buffer
	requestBuilder.Grow(len(url) + len(" HTTP/1.1") + len(crlf) + len(rest))
	requestBuilder.Write(url)
	requestBuilder.WriteString(" HTTP/1.1")
	requestBuilder.Write(crlf[:])
	requestBuilder.Write(rest)
	return requestBuilder.String()
}

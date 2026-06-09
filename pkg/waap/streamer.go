package waap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/minio/simdjson-go"
	"github.com/valyala/fastjson"
	"go.uber.org/zap"

	"github.com/pianoyeg94/waap/pkg/ctxt"
)

const (
	oneMB = 1024 * 1024

	maxRequestScanTokenSize = 300 * oneMB

	errRequestStreamerConsumingRequestStreamTemplate = "requestStreamer encountered error consuming http request stream %s: %w"
	errRequestStreamerFindingJsonTagTemplate         = "requestStreamer encountered error finding json tag %s: %w"
	errRequestStreamerParsingJsonTemplate            = "requestStreamer encountered error parsing json bytes %s: %w"
	errRequestStreamerBuildingRequestTemplate        = "requestStreamer encountered error building http request: %w"

	methodJsonTag = "method"
)

var (
	jsonParserPool  fastjson.ParserPool
	streamerBufPool = sync.Pool{
		New: func() any {
			return make([]byte, oneMB)
		},
	}
	requestBufPool = sync.Pool{
		New: func() any {
			return make([]byte, 0, oneMB)
		},
	}

	crlf       = [...]byte{'\r', '\n'}
	colonSpace = [...]byte{':', ' '}

	dataTag   = []byte(`"data"`)
	methodTag = []byte(`"method"`)
)

type requestStreamer struct {
	corpusStreams         <-chan *corpusStream
	logger                *zap.Logger
	processedStreamsSink  chan<- int
	processedRequestsSink chan<- string
	errorsSink            chan<- error
	isSIMDSupported       bool
	closeCtx              context.Context
}

func newRequestStreamer(
	corpusStreams <-chan *corpusStream,
	logger *zap.Logger,
	processedStreamsSink chan<- int,
	processedRequestsSink chan<- string,
	errorsSink chan<- error,
	isSIMDSupported bool,
	closeCtx context.Context,
) *requestStreamer {
	return &requestStreamer{
		corpusStreams:         corpusStreams,
		logger:                logger,
		processedStreamsSink:  processedStreamsSink,
		processedRequestsSink: processedRequestsSink,
		errorsSink:            errorsSink,
		isSIMDSupported:       isSIMDSupported,
		closeCtx:              closeCtx,
	}
}

func (s *requestStreamer) startStreamingRequestToSink(wg *sync.WaitGroup) {
	wg.Go(func() {
		defer func() {
			if err := recover(); err != nil {
				_ = ctxt.ErrorSendOrLog(s.errorsSink, fmt.Errorf(errWaapPanickedTemplate, err), s.logger, s.closeCtx)
			}
		}()
		for {
			select {
			case corpusSream, ok := <-s.corpusStreams:
				if !ok {
					return
				}
				requestCount, err := s.streamRequestsToSink(corpusSream)
				if err != nil {
					ctxt.ErrorSendOrLog(s.errorsSink, err, s.logger, s.closeCtx)
					return
				}
				select {
				case s.processedStreamsSink <- requestCount:
					if ctxt.ContextDone(s.closeCtx) {
						return
					}
				case <-s.closeCtx.Done():
					return
				}
			case <-s.closeCtx.Done():
				return
			}
		}
	})
}

func (s *requestStreamer) streamRequestsToSink(corpusStream *corpusStream) (requestCount int, err error) {
	cachedDataIdx := -1
	cachedMethodIdx := -1
	corpusStreamSplitFunc := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF {
			if len(data) > 0 {
				return len(data), data[:len(data)-1], nil
			}
			return 0, nil, bufio.ErrFinalToken
		}

		if cachedDataIdx == -1 {
			cachedDataIdx = bytes.Index(data, dataTag)
			if cachedDataIdx == -1 {
				return 0, nil, nil
			}
		}

		if cachedMethodIdx == -1 {
			cachedMethodIdx = bytes.Index(data[cachedDataIdx:], methodTag)
			if cachedMethodIdx == -1 {
				return 0, nil, nil
			}
		}

		var byteCountToPrevObject int
		for i := cachedDataIdx + cachedMethodIdx; i >= 0; i-- {
			byteCountToPrevObject++
			if data[i] == '}' {
				break
			}
		}

		advance = cachedDataIdx + cachedMethodIdx + 2 - byteCountToPrevObject + 2
		token = data[:cachedDataIdx+cachedMethodIdx+2-byteCountToPrevObject]
		cachedDataIdx = -1
		cachedMethodIdx = -1
		return advance, token, nil
	}

	stream := corpusStream.stream
	stream.Seek(1, 0)
	buf := streamerBufPool.Get().([]byte)
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(buf, maxRequestScanTokenSize)
	scanner.Split(corpusStreamSplitFunc)
	defer func() {
		streamerBufPool.Put(buf)
		stream.Close()
	}()

	for scanner.Scan() {
		var request string
		if request, err = s.buildRequest(scanner.Bytes()); err != nil {
			return requestCount, err
		}

		select {
		case s.processedRequestsSink <- request:
			requestCount++
			if ctxt.ContextDone(s.closeCtx) {
				return requestCount, nil
			}
		case <-s.closeCtx.Done():
			return requestCount, nil
		}
	}
	if scanner.Err() != nil {
		return requestCount, fmt.Errorf(errRequestStreamerConsumingRequestStreamTemplate, corpusStream.name, scanner.Err())
	}
	return requestCount, nil
}

func (s *requestStreamer) buildRequest(requestJsonObject []byte) (string, error) {
	if s.isSIMDSupported {
		return s.buildRequestSIMD(requestJsonObject)
	}
	return s.buildRequestPlain(requestJsonObject)
}

func writeHttpMethodSIMD(requestJsonObject *simdjson.Iter, httpRequestBuilder *bytes.Buffer, jsonElem *simdjson.Element) error {
	if _, err := requestJsonObject.FindElement(jsonElem, methodJsonTag); err != nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, methodJsonTag, err)
	}
	method, err := jsonElem.Iter.StringBytes()
	if err != nil {
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, methodJsonTag, err)
	}
	if _, err := httpRequestBuilder.Write(method); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func (s *requestStreamer) buildRequestSIMD(requestJsonObject []byte) (string, error) {
	parsedJson, err := simdjson.Parse(requestJsonObject, nil)
	if err != nil {
		return "", fmt.Errorf(errRequestStreamerParsingJsonTemplate, string(requestJsonObject), err)
	}

	buf := requestBufPool.Get().([]byte)
	httpBuilder := bytes.NewBuffer(buf)
	defer func() {
		buf = buf[:0]
		requestBufPool.Put(buf)
	}()

	if err = parsedJson.ForEach(func(iter simdjson.Iter) error {
		var method []byte
		var url []byte
		var headers simdjson.Element
		var data []byte

		var elem simdjson.Element
		var err error
		if err := writeHttpMethodSIMD(&iter, httpBuilder, &elem); err != nil {
			return err
		}
		if _, err = iter.FindElement(&elem, "method"); err != nil {
			return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, "method", err)
		}
		if method, err = elem.Iter.StringBytes(); err != nil {
			return fmt.Errorf(errRequestStreamerParsingJsonTemplate, "method", err)
		}
		if _, err = httpBuilder.Write(method); err != nil {
			return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}
		if err = httpBuilder.WriteByte(' '); err != nil {
			return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}

		if _, err := iter.FindElement(&elem, "url"); err != nil {
			return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, "url", err)
		}
		if url, err = elem.Iter.StringBytes(); err != nil {
			return fmt.Errorf(errRequestStreamerParsingJsonTemplate, "url", err)
		}
		if _, err = httpBuilder.Write(url); err != nil {
			return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}
		if _, err = httpBuilder.Write(crlf[:]); err != nil {
			return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}

		if _, err = iter.FindElement(&headers, "headers"); err != nil {
			return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, "headers", err)
		}
		var headersObj simdjson.Object
		if _, err = headers.Iter.Object(&headersObj); err != nil {
			return fmt.Errorf(errRequestStreamerParsingJsonTemplate, "headers", err)
		}
		if e := headersObj.ForEach(func(key []byte, i simdjson.Iter) {
			var headerVal []byte
			if _, err = httpBuilder.Write(key); err != nil {
				err = fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
				return
			}
			if _, err = httpBuilder.Write(colonSpace[:]); err != nil {
				err = fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
				return
			}
			if headerVal, err = i.StringBytes(); err != nil {
				err = fmt.Errorf(errRequestStreamerParsingJsonTemplate, "headers."+string(key), err)
				return
			}
			if _, err = httpBuilder.Write(headerVal); err != nil {
				err = fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
				return
			}
			if _, err = httpBuilder.Write(crlf[:]); err != nil {
				err = fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
				return
			}
		}, nil); e != nil || err != nil {
			return errors.Join(e, err)
		}
		if _, err = httpBuilder.Write(crlf[:]); err != nil {
			return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}

		if _, err := iter.FindElement(&elem, "data"); err != nil {
			if err == simdjson.ErrPathNotFound {
				return nil
			}
			return fmt.Errorf(errRequestStreamerParsingJsonTemplate, "data", err)
		}
		if data, err = elem.Iter.StringBytes(); err != nil {
			return err
		}
		if len(data) != 0 {
			if _, err = httpBuilder.Write(data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", err
	}
	return httpBuilder.String(), nil
}

func (s *requestStreamer) buildRequestPlain(requestJsonObject []byte) (string, error) {
	jsonParser := jsonParserPool.Get()
	defer func() { jsonParserPool.Put(jsonParser) }()

	jsonVal, err := fastjson.ParseBytes(requestJsonObject)
	if err != nil {
		return "", fmt.Errorf(errRequestStreamerParsingJsonTemplate, string(requestJsonObject), err)
	}

	method := jsonVal.GetStringBytes("method")
	if method == nil {
		return "", fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, "method", errors.New(""))
	}
	url := jsonVal.GetStringBytes("url")
	if url == nil {
		return "", fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, "url", errors.New(""))
	}
	headersObj := jsonVal.GetObject("headers")
	if headersObj == nil {
		return "", fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, "headers", errors.New(""))
	}
	data := jsonVal.GetStringBytes("data")

	buf := requestBufPool.Get().([]byte)
	httpBuilder := bytes.NewBuffer(buf)
	defer func() {
		buf = buf[:0]
		requestBufPool.Put(buf)
	}()

	if _, err = httpBuilder.Write(method); err != nil {
		return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if err = httpBuilder.WriteByte(' '); err != nil {
		return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err = httpBuilder.Write(url); err != nil {
		return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err = httpBuilder.Write(crlf[:]); err != nil {
		return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}

	headersObj.Visit(func(key []byte, v *fastjson.Value) {
		if err != nil {
			return
		}
		if _, err = httpBuilder.Write(key); err != nil {
			return
		}
		if _, err = httpBuilder.Write(colonSpace[:]); err != nil {
			return
		}
		if _, err = httpBuilder.Write(v.GetStringBytes()); err != nil {
			return
		}
		if _, err = httpBuilder.Write(crlf[:]); err != nil {
			return
		}
	})
	if err != nil {
		return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err = httpBuilder.Write(crlf[:]); err != nil {
		return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}

	if len(data) != 0 {
		if _, err = httpBuilder.Write(data); err != nil {
			return "", fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}
	}

	return httpBuilder.String(), nil
}

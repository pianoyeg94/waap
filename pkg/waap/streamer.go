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

	methodJsonTag  = "method"
	urlJsonTag     = "url"
	headersJsonTag = "headers"
	dataJsonTag    = "data"
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

func (s *requestStreamer) buildRequestSIMD(requestJsonObject []byte) (string, error) {
	parsedJson, err := simdjson.Parse(requestJsonObject, nil)
	if err != nil {
		return "", fmt.Errorf(errRequestStreamerParsingJsonTemplate, string(requestJsonObject), err)
	}

	buf := requestBufPool.Get().([]byte)
	httpRequestBuilder := bytes.NewBuffer(buf)
	defer func() {
		buf = buf[:0]
		requestBufPool.Put(buf)
	}()

	if err = parsedJson.ForEach(func(requestJsonObject simdjson.Iter) error {
		var elem simdjson.Element
		if err = writeHttpMethodSIMD(&requestJsonObject, httpRequestBuilder, &elem); err != nil {
			return err
		}
		if err = writeHttpPathSIMD(&requestJsonObject, httpRequestBuilder, &elem); err != nil {
			return err
		}
		if err = writeHttpHeadersSIMD(&requestJsonObject, httpRequestBuilder, &elem); err != nil {
			return err
		}
		return writeHttpBodySIMD(&requestJsonObject, httpRequestBuilder, &elem)
	}); err != nil {
		return "", err
	}
	return httpRequestBuilder.String(), nil
}

func (s *requestStreamer) buildRequestPlain(requestJsonObject []byte) (string, error) {
	jsonParser := jsonParserPool.Get()
	defer func() { jsonParserPool.Put(jsonParser) }()

	jsonObj, err := fastjson.ParseBytes(requestJsonObject)
	if err != nil {
		return "", fmt.Errorf(errRequestStreamerParsingJsonTemplate, string(requestJsonObject), err)
	}

	buf := requestBufPool.Get().([]byte)
	httpRequestBuilder := bytes.NewBuffer(buf)
	defer func() {
		buf = buf[:0]
		requestBufPool.Put(buf)
	}()

	if err = writeHttpMethodPlain(httpRequestBuilder, jsonObj); err != nil {
		return "", err
	}
	if err = writeHttpPathPlain(httpRequestBuilder, jsonObj); err != nil {
		return "", err
	}
	if err = writeHttpPathPlain(httpRequestBuilder, jsonObj); err != nil {
		return "", err
	}
	if err = writeHttpHeadersPlain(httpRequestBuilder, jsonObj); err != nil {
		return "", err
	}
	if err = writeHttpBodyPlain(httpRequestBuilder, jsonObj); err != nil {
		return "", err
	}
	return httpRequestBuilder.String(), nil
}

func writeHttpMethodPlain(httpRequestBuilder *bytes.Buffer, jsonObj *fastjson.Value) error {
	method := jsonObj.GetStringBytes(methodJsonTag)
	if method == nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, methodJsonTag, errors.New(""))
	}
	if _, err := httpRequestBuilder.Write(method); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if err := httpRequestBuilder.WriteByte(' '); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpPathPlain(httpRequestBuilder *bytes.Buffer, jsonObj *fastjson.Value) error {
	path := jsonObj.GetStringBytes(urlJsonTag)
	if path == nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, urlJsonTag, errors.New(""))
	}
	if _, err := httpRequestBuilder.Write(path); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err := httpRequestBuilder.Write(crlf[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpHeadersPlain(httpRequestBuilder *bytes.Buffer, jsonObj *fastjson.Value) (err error) {
	headersObj := jsonObj.GetObject(headersJsonTag)
	if headersObj == nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, headersJsonTag, errors.New(""))
	}

	headersObj.Visit(func(key []byte, val *fastjson.Value) {
		if err != nil {
			return
		}
		err = writeHttpHeaderPlain(httpRequestBuilder, key, val)
	})
	if err != nil {
		return err
	}
	if _, err = httpRequestBuilder.Write(crlf[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpBodyPlain(httpRequestBuilder *bytes.Buffer, jsonObj *fastjson.Value) error {
	data := jsonObj.GetStringBytes(dataJsonTag)
	if len(data) != 0 {
		if _, err := httpRequestBuilder.Write(data); err != nil {
			return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
		}
	}
	return nil
}

func writeHttpHeaderPlain(httpRequestBuilder *bytes.Buffer, headerKey []byte, headerVal *fastjson.Value) error {
	if _, err := httpRequestBuilder.Write(headerKey); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err := httpRequestBuilder.Write(colonSpace[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err := httpRequestBuilder.Write(headerVal.GetStringBytes()); err != nil {
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, headersJsonTag+"."+string(headerKey), err)
	}
	if _, err := httpRequestBuilder.Write(crlf[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpMethodSIMD(requestJsonObject *simdjson.Iter, httpRequestBuilder *bytes.Buffer, methodElem *simdjson.Element) error {
	if _, err := requestJsonObject.FindElement(methodElem, methodJsonTag); err != nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, methodJsonTag, err)
	}
	method, err := methodElem.Iter.StringBytes()
	if err != nil {
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, methodJsonTag, err)
	}
	if _, err := httpRequestBuilder.Write(method); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if err = httpRequestBuilder.WriteByte(' '); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpPathSIMD(requestJsonObject *simdjson.Iter, httpRequestBuilder *bytes.Buffer, pathElem *simdjson.Element) error {
	if _, err := requestJsonObject.FindElement(pathElem, urlJsonTag); err != nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, urlJsonTag, err)
	}
	path, err := pathElem.Iter.StringBytes()
	if err != nil {
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, urlJsonTag, err)
	}
	if _, err = httpRequestBuilder.Write(path); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err = httpRequestBuilder.Write(crlf[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpHeadersSIMD(requestJsonObject *simdjson.Iter, httpRequestBuilder *bytes.Buffer, headersElem *simdjson.Element) (err error) {
	if _, err = requestJsonObject.FindElement(headersElem, headersJsonTag); err != nil {
		return fmt.Errorf(errRequestStreamerFindingJsonTagTemplate, headersJsonTag, err)
	}
	var headersObj simdjson.Object
	if _, err = headersElem.Iter.Object(&headersObj); err != nil {
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, headersJsonTag, err)
	}
	if e := headersObj.ForEach(func(key []byte, val simdjson.Iter) {
		if err != nil {
			return
		}
		err = writeHttpHeaderSIMD(httpRequestBuilder, key, &val)
	}, nil); e != nil || err != nil {
		return errors.Join(e, err)
	}
	if _, err = httpRequestBuilder.Write(crlf[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

func writeHttpBodySIMD(requestJsonObject *simdjson.Iter, httpRequestBuilder *bytes.Buffer, bodyElem *simdjson.Element) error {
	if _, err := requestJsonObject.FindElement(bodyElem, dataJsonTag); err != nil {
		if errors.Is(err, simdjson.ErrPathNotFound) {
			return nil
		}
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, dataJsonTag, err)
	}
	data, err := bodyElem.Iter.StringBytes()
	if err != nil {
		return err
	}
	if len(data) != 0 {
		if _, err = httpRequestBuilder.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func writeHttpHeaderSIMD(
	httpRequestBuilder *bytes.Buffer,
	headerKey []byte,
	headerValElem *simdjson.Iter,
) error {
	if _, err := httpRequestBuilder.Write(headerKey); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err := httpRequestBuilder.Write(colonSpace[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)

	}
	headerVal, err := headerValElem.StringBytes()
	if err != nil {
		return fmt.Errorf(errRequestStreamerParsingJsonTemplate, headersJsonTag+"."+string(headerKey), err)
	}
	if _, err = httpRequestBuilder.Write(headerVal); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	if _, err = httpRequestBuilder.Write(crlf[:]); err != nil {
		return fmt.Errorf(errRequestStreamerBuildingRequestTemplate, err)
	}
	return nil
}

package waap

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"

	"github.com/dgraph-io/badger/v4"
	"go.uber.org/zap"
	protobuf "google.golang.org/protobuf/proto"

	"github.com/pianoyeg94/waap/pkg/ctxt"
	"github.com/pianoyeg94/waap/pkg/strs"
	"github.com/pianoyeg94/waap/proto"
)

const (
	errNmapProberMarshallingProbeTemplate      = "nmapProber encountered error marshalling nmap probe to protobuf: %w"
	errNmapProberDumpingProbesToBadgerTemplate = "nmapProber encountered error dummping nmap probes to BadgerDB: %w"
)

type nmapProber struct {
	nmapProbes                nmapProbes
	nmapProbeAccessRandomizer *nmapProbeAccessRandomizer
	badgerDB                  *badger.DB
	badgerKey                 *uint64
	logger                    *zap.Logger
	requestsSink              <-chan string
	errorsSink                chan<- error
	processedRequestEvent     chan<- struct{}

	closeCtx context.Context
}

func newNmapProber(
	nmapProbes nmapProbes,
	badgerDB *badger.DB,
	badgerKey *uint64,
	logger *zap.Logger,
	requestsSink <-chan string,
	errorsSink chan<- error,
	processedRequestEvent chan<- struct{},
	closeCtx context.Context,
) *nmapProber {
	return &nmapProber{
		nmapProbes:                nmapProbes,
		nmapProbeAccessRandomizer: newNmapProbeAccessRandomizer(nmapProbes),
		badgerDB:                  badgerDB,
		badgerKey:                 badgerKey,
		logger:                    logger,
		requestsSink:              requestsSink,
		errorsSink:                errorsSink,
		processedRequestEvent:     processedRequestEvent,
		closeCtx:                  closeCtx,
	}
}

func (p *nmapProber) startProbingRequests(wg *sync.WaitGroup) {
	wg.Go(func() {
		probeCache := make([][]byte, 0, 100)
		defer func() {
			if err := recover(); err != nil {
				_ = ctxt.ErrorSendOrLog(p.errorsSink, fmt.Errorf(errWaapPanickedTemplate, err), p.logger, p.closeCtx)
				return
			}
			if len(probeCache) > 0 {
				if err := p.dumpProbeCacheToBadger(probeCache); err != nil {
					_ = ctxt.ErrorSendOrLog(p.errorsSink, err, p.logger, p.closeCtx)
				}
			}
		}()
		for {
			select {
			case request := <-p.requestsSink:
				probeBytes, err := p.probeRequest(request, p.closeCtx)
				if err != nil {
					if ctxDone := ctxt.ErrorSendOrLog(p.errorsSink, err, p.logger, p.closeCtx); ctxDone {
						return
					}
				}
				if probeBytes == nil {
					return
				}

				probeCache = append(probeCache, probeBytes)
				select {
				case p.processedRequestEvent <- struct{}{}:
					if ctxt.ContextDone(p.closeCtx) {
						return
					}
				case <-p.closeCtx.Done():
					return
				}

				if len(probeCache) == cap(probeCache) {
					if err := p.dumpProbeCacheToBadger(probeCache); err != nil {
						if ctxDone := ctxt.ErrorSendOrLog(p.errorsSink, err, p.logger, p.closeCtx); ctxDone {
							return
						}
						probeCache = probeCache[:0]
					}
				}
			case <-p.closeCtx.Done():
				return
			}
		}
	})
}

func (p *nmapProber) probeRequest(request string, ctx context.Context) (marshalledProbe []byte, err error) {
	var requestProbe requestProbe
	requestProbe.httpRequest = request
	for _, rarity := range p.nmapProbeAccessRandomizer.randomizedRarities {
		if ctxt.ContextDone(ctx) {
			return nil, nil
		}
		for _, probeIdx := range p.nmapProbeAccessRandomizer.randomizedProbeIndexes {
			if ctxt.ContextDone(ctx) {
				return nil, nil
			}
			isNextProbe := true
			probe := p.nmapProbes[rarity][probeIdx]
			for _, regexIdx := range p.nmapProbeAccessRandomizer.randomizedRegexParserIndexes {
				regexParser := probe.regexParsers[regexIdx]
				regexParser.mx.Lock()
				matcher := regexParser.parser.MatcherString(request, 0)
				errcode := matcher.ExecString(request, 0)
				matches := matcher.Matches()
				matcher.Free()
				regexParser.mx.Unlock()
				if errcode >= 0 && matches {
					populateRequestProbe(&requestProbe, probe, isNextProbe, regexParser, rarity)
					isNextProbe = false
				}
			}
		}
	}
	return vtprotoPooledMarshalRequestProbe(&requestProbe)
}

func (p *nmapProber) dumpProbeCacheToBadger(probeCache [][]byte) error {
	wb := p.badgerDB.NewWriteBatch()
	defer wb.Cancel()

	for _, probe := range probeCache {
		key := atomic.AddUint64(p.badgerKey, 1)
		keyBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(keyBytes, key)
		if err := wb.Set(keyBytes, probe); err != nil {
			return fmt.Errorf(errNmapProberDumpingProbesToBadgerTemplate, err)
		}
	}
	if err := wb.Flush(); err != nil {
		return fmt.Errorf(errNmapProberDumpingProbesToBadgerTemplate, err)
	}
	return nil
}

func marshalRequestProbe(requestProbe *requestProbe) ([]byte, error) {
	var requestProbeProto proto.RequestProbe
	convertRequestProbeToProto(requestProbe, &requestProbeProto)
	requestProbeBytes, err := protobuf.Marshal(&requestProbeProto)
	if err != nil {
		return nil, fmt.Errorf(errNmapProberMarshallingProbeTemplate, err)
	}
	return requestProbeBytes, nil
}

func vtprotoMarshalRequestProbe(requestProbe *requestProbe) ([]byte, error) {
	var requestProbeProto proto.RequestProbe
	convertRequestProbeToProto(requestProbe, &requestProbeProto)
	requestProbeBytes, err := requestProbeProto.MarshalVT()
	if err != nil {
		return nil, fmt.Errorf(errNmapProberMarshallingProbeTemplate, err)
	}
	return requestProbeBytes, nil
}

func vtprotoPooledMarshalRequestProbe(requestProbe *requestProbe) ([]byte, error) {
	requestProbeProto := proto.RequestProbeFromVTPool()
	convertRequestProbeToProto(requestProbe, requestProbeProto)
	defer requestProbeProto.ReturnToVTPool()
	requestProbeBytes, err := requestProbeProto.MarshalVT()
	if err != nil {
		return nil, fmt.Errorf(errNmapProberMarshallingProbeTemplate, err)
	}
	return requestProbeBytes, nil
}

// func fastpbMarshalRequestProbe(requestProbe *requestProbe) ([]byte, error) {
// 	var requestProbeProto proto.RequestProbe
// 	convertRequestProbeToProto(requestProbe, &requestProbeProto)
// 	requestProbeBytes := make([]byte, requestProbeProto.Size())
// 	requestProbeProto.FastWrite(requestProbeBytes)
// 	return requestProbeBytes, nil
// }

func convertRequestProbeToProto(probe *requestProbe, protoProbe *proto.RequestProbe) {
	protoProbe.HttpRequest = probe.httpRequest
	for _, match := range probe.nullProbe {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity1 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity2 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity3 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity4 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity5 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity6 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity7 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity8 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
	for _, match := range probe.rarity9 {
		protoProbe.NullProbe = append(protoProbe.NullProbe, &proto.Match{
			ProbeName:      match.probeName,
			Matches:        strs.StringSetToSlice(match.uniqueMatches),
			MatchCount:     match.matchCount,
			Softmatches:    strs.StringSetToSlice(match.uniqueSoftMatches),
			SoftmatchCount: match.softMatchCount,
		})
	}
}

func populateRequestProbe(probe *requestProbe, nmapProbe *nmapProbe, isNextProbe bool, regexParser *regexParser, rarity int) {
	populateRequestProbeMatches(probe, nmapProbe, isNextProbe, regexParser, rarity)
}

func populateRequestProbeMatches(probe *requestProbe, nmapProbe *nmapProbe, isNextProbe bool, regexParser *regexParser, rarity int) {
	switch rarity {
	case 0:
		if isNextProbe {
			probe.nullProbe = append(probe.nullProbe, &match{})
		}
		populateRequestProbeMatch(probe.nullProbe[len(probe.nullProbe)-1], nmapProbe, regexParser)
	case 1:
		if isNextProbe {
			probe.rarity1 = append(probe.rarity1, &match{})
		}
		populateRequestProbeMatch(probe.rarity1[len(probe.rarity1)-1], nmapProbe, regexParser)
	case 2:
		if isNextProbe {
			probe.rarity2 = append(probe.rarity2, &match{})
		}
		populateRequestProbeMatch(probe.rarity2[len(probe.rarity2)-1], nmapProbe, regexParser)
	case 3:
		if isNextProbe {
			probe.rarity3 = append(probe.rarity3, &match{})
		}
		populateRequestProbeMatch(probe.rarity3[len(probe.rarity3)-1], nmapProbe, regexParser)
	case 4:
		if isNextProbe {
			probe.rarity4 = append(probe.rarity4, &match{})
		}
		populateRequestProbeMatch(probe.rarity4[len(probe.rarity4)-1], nmapProbe, regexParser)
	case 5:
		if isNextProbe {
			probe.rarity5 = append(probe.rarity5, &match{})
		}
		populateRequestProbeMatch(probe.rarity5[len(probe.rarity5)-1], nmapProbe, regexParser)
	case 6:
		if isNextProbe {
			probe.rarity6 = append(probe.rarity6, &match{})
		}
		populateRequestProbeMatch(probe.rarity6[len(probe.rarity6)-1], nmapProbe, regexParser)
	case 7:
		if isNextProbe {
			probe.rarity7 = append(probe.rarity7, &match{})
		}
		populateRequestProbeMatch(probe.rarity7[len(probe.rarity7)-1], nmapProbe, regexParser)
	case 8:
		if isNextProbe {
			probe.rarity8 = append(probe.rarity8, &match{})
		}
		populateRequestProbeMatch(probe.rarity8[len(probe.rarity8)-1], nmapProbe, regexParser)
	case 9:
		if isNextProbe {
			probe.rarity9 = append(probe.rarity9, &match{})
		}
		populateRequestProbeMatch(probe.rarity9[len(probe.rarity9)-1], nmapProbe, regexParser)
	}
}

func populateRequestProbeMatch(match *match, nmapProbe *nmapProbe, regexParser *regexParser) {
	match.probeName = nmapProbe.name
	switch regexParser.matchType {
	case regexMatch:
		match.matchCount++
		if match.uniqueMatches == nil {
			match.uniqueMatches = make(map[string]struct{})
		}
		match.uniqueMatches[regexParser.serviceName] = struct{}{}
	case regexSoftMatch:
		match.softMatchCount++
		if match.uniqueSoftMatches == nil {
			match.uniqueSoftMatches = make(map[string]struct{})
		}
		match.uniqueSoftMatches[regexParser.serviceName] = struct{}{}
	}
}

type nmapProbeAccessRandomizer struct {
	randomizedRarityIndexes randomizedRarityIndexes
	currentRarity           int
	currentProbeIdx         int
}

func newNmapProbeAccessRandomizer(nmapProbes nmapProbes) *nmapProbeAccessRandomizer {
	var nmapProbeAccessRandomizer nmapProbeAccessRandomizer
	randomizedRarityIndexes := randomizedRarityIndexes{
		randomizedIndexes:      rand.Perm(len(nmapProbes)),
		randomizedProbeIndexes: make([]randomizedProbeIndexes, len(nmapProbes)),
	}
	for _, rarity := range randomizedRarityIndexes.randomizedIndexes {
		randomizedRarityIndexes.randomizedProbeIndexes[rarity].randomizedIndexes = rand.Perm(len(nmapProbes[rarity]))
		for _, probeIdx := range randomizedRarityIndexes.randomizedProbeIndexes[rarity].randomizedIndexes {
			randomizedRarityIndexes.randomizedProbeIndexes[rarity].randomizedRegexIndexes = make(
				[]randomizedRegexIndexes,
				len(nmapProbes[rarity]),
			)
			for range nmapProbes[rarity][probeIdx].regexParsers {
				randomizedRarityIndexes.randomizedProbeIndexes[rarity].randomizedRegexIndexes[probeIdx].randomizedIndexes = rand.Perm(
					len(nmapProbes[rarity][probeIdx].regexParsers),
				)
			}
		}
	}
	nmapProbeAccessRandomizer.randomizedRarityIndexes = randomizedRarityIndexes
	return &nmapProbeAccessRandomizer

}

func (r *nmapProbeAccessRandomizer) randomizedRarities(yield func(int, int) bool) {
	for i, rarity := range r.randomizedRarityIndexes.randomizedIndexes {
		r.currentRarity = rarity
		if !yield(i, rarity) {
			return
		}
	}
}

func (r *nmapProbeAccessRandomizer) randomizedProbeIndexes(yield func(int, int) bool) {
	for i, probeIdx := range r.randomizedRarityIndexes.randomizedProbeIndexes[r.currentRarity].randomizedIndexes {
		r.currentProbeIdx = probeIdx
		if !yield(i, probeIdx) {
			return
		}
	}
}

func (r *nmapProbeAccessRandomizer) randomizedRegexParserIndexes(yield func(int, int) bool) {
	for i, regexParserIdx := range r.randomizedRarityIndexes.randomizedProbeIndexes[r.currentRarity].randomizedRegexIndexes[r.currentProbeIdx].randomizedIndexes {
		if !yield(i, regexParserIdx) {
			return
		}
	}
}

type randomizedRarityIndexes struct {
	randomizedIndexes      []int
	randomizedProbeIndexes []randomizedProbeIndexes
}

type randomizedProbeIndexes struct {
	randomizedIndexes      []int
	randomizedRegexIndexes []randomizedRegexIndexes
}

type randomizedRegexIndexes struct {
	randomizedIndexes []int
}

type requestProbe struct {
	httpRequest string
	nullProbe   []*match
	rarity1     []*match
	rarity2     []*match
	rarity3     []*match
	rarity4     []*match
	rarity5     []*match
	rarity6     []*match
	rarity7     []*match
	rarity8     []*match
	rarity9     []*match
}

type match struct {
	probeName         string
	uniqueMatches     map[string]struct{}
	matchCount        uint64
	uniqueSoftMatches map[string]struct{}
	softMatchCount    uint64
}

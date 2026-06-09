package waap

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	pcre "github.com/Jemmic/go-pcre2"

	"github.com/pianoyeg94/waap/pkg/bts"
	"github.com/pianoyeg94/waap/pkg/strs"
)

const (
	nmapRarityCount = 10

	regexInvalidNestedRepetionOperatorErrMsg = "quantifier does not follow a repeatable item"

	errParsingNmapProbeTokenTemplate               = "nmapParser encountered error parsing 'probe' token: %w"
	errParsingNmapRarityTokenTemplate              = "nmapParser encountered error parsing 'rarity' token: %w"
	errParsingNmapFileTemplate                     = "nmapParser encountered error parsing file: %w"
	errNmapParserCompilingRegexTemplate            = "nmapParser encountered error compiling regex: %w"
	errNmapParserFixingNestedRepetionRegexTemplate = "nmapParser encountered error fixing nested repetion in regex: %w"
)

var (
	whitespace                = [...]byte{' '}
	doubleStar                = [...]byte{'*', '*'}
	doublePlus                = [...]byte{'+', '+'}
	escapedDoubleStar         = []byte(`*\*`)
	escapedDoubleStarReverted = []byte(`\**`)
	escapedDoublePlus         = []byte(`\++`)

	ErrNmapAlreadyParsed      = errors.New("nmapParser already closed")
	ErrParsingNmapProbeToken  = errors.New("nmapParser encountered error parsing 'probe' token")
	ErrParsingNmapRarityToken = errors.New("nmapParser encountered error parsing 'rarity' token")
	ErrParsingNmapRegexToken  = errors.New("nmapParser encountered error parsing 'regex' token")
)

type regexMatchType uint64

const (
	regexMatch regexMatchType = iota
	regexSoftMatch
)

func regexMatchTypeToString(matchType regexMatchType) string {
	switch matchType {
	case regexMatch:
		return "match"
	case regexSoftMatch:
		return "softmatch"
	}
	return ""
}

type nmapProbes [][]*nmapProbe

type nmapParser struct {
	nmapFile      io.ReadCloser
	atProbeToken  bool
	atRarityToken bool
	isClosed      bool
}

func newNmapParser(nmapFile io.ReadCloser) *nmapParser {
	return &nmapParser{
		nmapFile: nmapFile,
	}
}

func (p *nmapParser) parse() ([][]*nmapProbe, error) {
	if p.isClosed {
		return nil, ErrNmapAlreadyParsed
	}

	buf := make([]byte, 0, 4*1024)
	scanner := bufio.NewScanner(p.nmapFile)
	scanner.Buffer(buf, bufio.MaxScanTokenSize)
	scanner.Split(p.nmapSlitFunc)
	defer func() {
		p.nmapFile.Close()
		p.isClosed = true
	}()

	probes := make([][]*nmapProbe, nmapRarityCount)
	probe := defaultNmapProbe()
	for scanner.Scan() {
		token := scanner.Bytes()
		if p.atProbeToken {
			if probe.rarity != -1 {
				probes[probe.rarity] = append(probes[probe.rarity], probe)
				probe = defaultNmapProbe()
			}
			if err := p.populateNmapProbeWithName(token, probe); err != nil {
				return nil, err
			}
		} else if p.atRarityToken {
			if err := p.populateNmapProbeWithRarity(token, probe); err != nil {
				return nil, err
			}
		} else {
			if err := p.populateNmapProbeWithRegexParsers(token, probe); err != nil {
				return nil, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf(errParsingNmapFileTemplate, err)
	}

	return probes, nil
}

func (p *nmapParser) populateNmapProbeWithName(probeToken []byte, probe *nmapProbe) error {
	probeNameTuple := bytes.SplitN(probeToken, whitespace[:], 4)
	if len(probeNameTuple) < 4 {
		return ErrParsingNmapProbeToken
	}

	namePart1, namePart2 := probeNameTuple[1], probeNameTuple[2]
	if bts.BytesToStringZeroCopy(namePart2) == "NULL" {
		probe.rarity = 0
	}

	var nameBuilder bytes.Buffer
	nameBuilder.Grow(len(namePart1) + len(" ") + len(namePart2))
	if _, err := nameBuilder.Write(namePart1); err != nil {
		return fmt.Errorf(errParsingNmapProbeTokenTemplate, err)
	}
	if err := nameBuilder.WriteByte(' '); err != nil {
		return fmt.Errorf(errParsingNmapProbeTokenTemplate, err)
	}
	if _, err := nameBuilder.Write(namePart2); err != nil {
		return fmt.Errorf(errParsingNmapProbeTokenTemplate, err)
	}
	probe.name = nameBuilder.String()

	return nil
}

func (p *nmapParser) populateNmapProbeWithRarity(rarityToken []byte, probe *nmapProbe) error {
	rarityTuple := bytes.SplitN(rarityToken, whitespace[:], 2)
	if len(rarityTuple) < 2 {
		return ErrParsingNmapRarityToken
	}

	rarityStr := bts.BytesToStringZeroCopy(rarityTuple[1])
	rarity, err := strconv.Atoi(rarityStr)
	if err != nil {
		return fmt.Errorf(errParsingNmapRarityTokenTemplate, err)
	}

	probe.rarity = rarity
	return nil
}

func (p *nmapParser) populateNmapProbeWithRegexParsers(regexToken []byte, probe *nmapProbe) error {
	regexTuple := bytes.SplitN(regexToken, whitespace[:], 3)
	if len(regexTuple) < 3 {
		return ErrParsingNmapRegexToken
	}

	matchTypeBytes, serviceName, regex := regexTuple[0], regexTuple[1], regexTuple[2]
	matchType := regexMatch
	if bts.BytesToStringZeroCopy(matchTypeBytes) == regexMatchTypeToString(regexSoftMatch) {
		matchType = regexSoftMatch
	}

	regexStr := string(regex)
	var parser *pcre.Regexp
	var err error
	for range 2 {
		parser, err = pcre.CompileJIT(regexStr, 0, 0)
		if err == nil {
			break
		}
		err = fmt.Errorf(errNmapParserCompilingRegexTemplate, err)
		if !strings.Contains(err.Error(), regexInvalidNestedRepetionOperatorErrMsg) {
			return err
		}
		if regexStr, err = fixRegexWithNestedRepetionOperator(regex, err); err != nil {
			return err
		}
	}
	probe.regexParsers = append(probe.regexParsers, &regexParser{
		parser:      parser,
		serviceName: string(serviceName),
		matchType:   matchType,
	})

	return nil
}

func (p *nmapParser) nmapSlitFunc(data []byte, atEOF bool) (advance int, token []byte, err error) {
	p.atProbeToken, p.atRarityToken = false, false
	advance, token, err = bufio.ScanLines(data, atEOF)
	if err != nil {
		return 0, nil, err
	} else if advance == 0 {
		return advance, token, err
	} else {
		tokenStr := bts.BytesToStringZeroCopy(token)
		if strings.HasPrefix(tokenStr, "Probe") {
			p.atProbeToken = true
		} else if strings.HasPrefix(tokenStr, "rarity") {
			p.atRarityToken = true
		} else if !(strings.HasPrefix(tokenStr, "match") || strings.HasPrefix(tokenStr, "softmatch")) {
			return advance, nil, nil
		}
		return advance, strs.StringToBytesZeroCopy(tokenStr), nil
	}
}

func fixRegexWithNestedRepetionOperator(regex []byte, err error) (string, error) {
	var regexBuf bytes.Buffer
	regexBuf.Grow(len(regex) + 2)
	regexParts := bytes.SplitN(regex, doubleStar[:], 3)
	if len(regexParts) == 3 {
		if _, e := regexBuf.Write(regexParts[0]); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		if _, e := regexBuf.Write(escapedDoubleStar); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		if _, e := regexBuf.Write(regexParts[1]); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		if _, e := regexBuf.Write(escapedDoubleStarReverted); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		if _, e := regexBuf.Write(regexParts[2]); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		return regexBuf.String(), nil
	}
	regexParts = bytes.SplitN(regex, doublePlus[:], 2)
	if len(regexParts) == 2 {
		if _, e := regexBuf.Write(regexParts[0]); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		if _, e := regexBuf.Write(escapedDoublePlus); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		if _, e := regexBuf.Write(regexParts[1]); e != nil {
			return "", fmt.Errorf(errNmapParserFixingNestedRepetionRegexTemplate, e)
		}
		return regexBuf.String(), nil
	}
	return "", err
}

type nmapProbe struct {
	name         string
	rarity       int
	regexParsers []*regexParser
}

func defaultNmapProbe() *nmapProbe {
	return &nmapProbe{
		name:         "",
		rarity:       -1,
		regexParsers: make([]*regexParser, 0, 20),
	}
}

type regexParser struct {
	parser      *pcre.Regexp
	serviceName string
	matchType   regexMatchType
	mx          sync.Mutex
}

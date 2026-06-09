package waap

import (
	"os"
	"testing"
)

func TestNmapParserParse(t *testing.T) {
	nmapProbesFile, err := os.Open("./test-data/nmap-service-probes")
	if err != nil {
		t.Fatalf("Failed opening nmap-service-probes file: %v", err)
	}

	nmapParser := newNmapParser(nmapProbesFile)
	nmapProbes, err := nmapParser.parse()
	if err != nil {
		t.Fatalf("Failed parsing nmap probes: %v", err)
	}

	const totalRarityCountIncludingNULLProbe = 10
	if len(nmapProbes) != totalRarityCountIncludingNULLProbe {
		t.Errorf("nmapParser.parse() rarity count including NULL probe = %d, want %d", len(nmapProbes), totalRarityCountIncludingNULLProbe)
	}
}

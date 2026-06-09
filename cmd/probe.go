package cmd

import (
	"context"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/pianoyeg94/waap/pkg/cpu"
	"github.com/pianoyeg94/waap/pkg/waap"
)

var (
	nmapProbesFilePath  string
	httpCorpusesDirPath string
	badgerDBDataDirPath string
	cpuPercentage       float64
	cpuCoresLimit       int
	probeNumRequestsMax int

	probeCmd = &cobra.Command{
		Use:   "probe",
		Long:  "Run nmap probes against http request corpuses",
		Short: "Run nmap probes against http request corpuses",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := cpu.LimitCpuUsagePercentage(cpuPercentage); err != nil {
				return err
			}
			return cpu.LimitCpuCoresUsage(cpuCoresLimit)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			go func() { cmd.Parent().RunE(cmd, args) }() // run pprof server

			logger := cmd.Context().Value(LoggerKey).(*zap.Logger)
			ctxCancel := cmd.Context().Value(ContextCancelFuncKey).(context.CancelFunc)
			defer ctxCancel()

			nmapFile, err := os.Open(nmapProbesFilePath)
			if err != nil {
				return err
			}

			wp, err := waap.NewWaap(nmapFile, httpCorpusesDirPath, badgerDBDataDirPath, probeNumRequestsMax, cpu.DoesCPUSuppportSIMD(), logger, cmd.Context())
			if err != nil {
				return err
			}
			wp.StartProbingRequests()

			select {
			case <-cmd.Context().Done():
			case <-wp.FinishedSignal():
			}
			return wp.Close()
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			return cpu.TearDownCpuUsageLimits()
		},
	}
)

func init() {
	probeCmd.Flags().StringVar(&nmapProbesFilePath, "nmap-probes-file", "./nmap-service-probes", "Path to nmap-service-probes file")
	probeCmd.Flags().StringVar(&httpCorpusesDirPath, "http-corpuses-dir", "./http-corpuses", "Path to http corpuses directory")
	probeCmd.Flags().StringVar(&badgerDBDataDirPath, "badger-data-dir", "./badger", "Path to BadgerDB data directory")
	probeCmd.Flags().Float64Var(&cpuPercentage, "cpu-percentage", 90.0, "Restrict running process's CPU usage")
	probeCmd.Flags().IntVar(&cpuCoresLimit, "cpu-cores-limit", runtime.NumCPU(), "Restrict running process to a subset of available CPU cores")
	probeCmd.Flags().IntVar(&probeNumRequestsMax, "probe-num-requests", -1, "Probe only a subset of http requests")
	rootCmd.AddCommand(probeCmd)
}

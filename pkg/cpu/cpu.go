package cpu

import (
	"fmt"
	"os"
	"runtime"

	"github.com/containerd/cgroups/v3/cgroup2"
	"github.com/minio/simdjson-go"
)

const waapCgroupPath = "/waap"

func LimitCpuUsagePercentage(percentage float64) error {
	// 1. Query the number of logical CPUs on the machine
	numCPUs := float64(runtime.NumCPU())

	// 2. Set a base period of 100,000 microseconds (100ms)
	const period uint64 = 100000

	// 3. Calculate the required quota across all available CPUs
	// Formula: (percentage / 100) * total CPUs * period
	quotaFloat := (percentage / 100.0) * numCPUs * float64(period)
	quota := int64(quotaFloat)

	// 4. Construct the resource rule string for Cgroups v2 ("quota period")
	// If quota is 200000 and period is 100000, it writes "200000 100000"
	maxLimitString := cgroup2.CPUMax(fmt.Sprintf("%d %d", quota, period))
	resources := &cgroup2.Resources{
		CPU: &cgroup2.CPU{
			Max: maxLimitString,
		},
	}
	// 5. Create or join the target control group hierarchy
	// Note: Root privileges or delegated cgroup permissions are required to write
	manager, err := cgroup2.NewManager("/sys/fs/cgroup", waapCgroupPath, resources)
	if err != nil {
		return err
	}

	// 6. Attach the current running process (PID) to this resource envelope
	if err := manager.AddProc(uint64(os.Getpid())); err != nil {
		manager.Delete()
		return err
	}

	return nil
}

func LimitCpuCoresUsage(numCores int) error {
	numCpus := runtime.NumCPU()
	if numCores >= numCpus {
		return nil
	}

	var cpusetCpus string
	if numCores == 1 {
		cpusetCpus = "0"
	} else {
		cpusetCpus = fmt.Sprintf("0-%d", numCores-1)
	}

	resources := &cgroup2.Resources{
		CPU: &cgroup2.CPU{
			Cpus: cpusetCpus,
		},
	}
	manager, err := cgroup2.NewManager("/sys/fs/cgroup", waapCgroupPath, resources)
	if err != nil {
		return err
	}

	defer func() {
		runtime.GOMAXPROCS(numCores)
	}()
	pid := os.Getpid()
	path, err := cgroup2.PidGroupPath(pid)
	if err != nil {
		return err
	}
	if path == waapCgroupPath {
		return nil
	}

	if err := manager.AddProc(uint64(pid)); err != nil {
		manager.Delete()
		return err
	}

	return nil
}

func TearDownCpuUsageLimits() error {
	pid := os.Getpid()
	path, err := cgroup2.PidGroupPath(pid)
	if err != nil {
		return err
	}
	if path != waapCgroupPath {
		return nil
	}

	waapManager, err := cgroup2.Load(waapCgroupPath)
	if err != nil {
		return err
	}
	rootManager, err := cgroup2.Load("/")
	if err != nil {
		return err
	}

	if err = rootManager.AddProc(uint64(pid)); err != nil {
		return err
	}
	return waapManager.Delete()
}

func DoesCPUSuppportSIMD() bool {
	return simdjson.SupportedCPU()
}

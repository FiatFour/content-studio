package api

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
)

type cpuSample struct{ idle, total uint64 }

func readCPU() (cpuSample, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuSample{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan()
	fields := strings.Fields(sc.Text())
	var s cpuSample
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		s.total += n
		if i == 3 || i == 4 {
			s.idle += n
		}
	}
	return s, nil
}

func readMem() (totalMiB, availMiB int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			totalMiB = kb / 1024
		case "MemAvailable":
			availMiB = kb / 1024
		}
	}
	return totalMiB, availMiB
}

func readVRAM(ctx context.Context) (used, total int64, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=memory.used,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, 0, false
	}
	a, b, found := strings.Cut(strings.TrimSpace(strings.Split(string(out), "\n")[0]), ",")
	if !found {
		return 0, 0, false
	}
	used, err1 := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	total, err2 := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
	return used, total, err1 == nil && err2 == nil
}

// system reports measured machine state for the top bar and the "this machine" card.
// A value that cannot be read is null, never a guess.
func (s *Server) system(c echo.Context) error {
	ctx := c.Request().Context()
	resp := map[string]any{}
	if a, err := readCPU(); err == nil {
		time.Sleep(200 * time.Millisecond)
		if b, err := readCPU(); err == nil && b.total > a.total {
			resp["cpu_pct"] = 100 * float64((b.total-a.total)-(b.idle-a.idle)) / float64(b.total-a.total)
		}
	}
	if total, avail := readMem(); total > 0 {
		resp["ram_used_mib"], resp["ram_total_mib"] = total-avail, total
	}
	if used, total, ok := readVRAM(ctx); ok {
		resp["vram_used_mib"], resp["vram_total_mib"] = used, total
	}
	var fs syscall.Statfs_t
	if root, err := s.Store.Abs("."); err == nil && syscall.Statfs(root, &fs) == nil {
		resp["disk_free_gib"] = float64(fs.Bavail*uint64(fs.Bsize)) / (1 << 30)
		resp["disk_total_gib"] = float64(fs.Blocks*uint64(fs.Bsize)) / (1 << 30)
	}
	var gpuStage, gpuEpisode *string
	var runningJobs, today int
	err := s.DB.QueryRow(ctx, `SELECT
			(SELECT j.stage FROM jobs j WHERE j.state = 'running' AND j.stage IN ('script', 'images', 'tts') LIMIT 1),
			(SELECT e.title FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN episodes e ON e.id = r.episode_id
				WHERE j.state = 'running' AND j.stage IN ('script', 'images', 'tts') LIMIT 1),
			(SELECT count(*) FROM jobs WHERE state = 'running'),
			(SELECT count(DISTINCT episode_id) FROM artifacts a WHERE a.type = 'video' AND a.validation_status = 'qa_passed'
				AND a.created_at >= date_trunc('day', now() AT TIME ZONE 'Asia/Bangkok') AT TIME ZONE 'Asia/Bangkok')`).
		Scan(&gpuStage, &gpuEpisode, &runningJobs, &today)
	if err != nil {
		return err
	}
	resp["gpu_slot_stage"], resp["gpu_slot_episode"] = gpuStage, gpuEpisode
	resp["running_jobs"], resp["episodes_ready_today"] = runningJobs, today
	resp["cloud_spend"] = nil
	return c.JSON(http.StatusOK, resp)
}

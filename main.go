// gitslap listens to the Apple Silicon accelerometer and maps physical
// gestures to git commands. Needs sudo.
package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/fang"
	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/spf13/cobra"
	"github.com/taigrr/apple-silicon-accelerometer/detector"
	"github.com/taigrr/apple-silicon-accelerometer/sensor"
	"github.com/taigrr/apple-silicon-accelerometer/shm"
)

var version = "dev"

//go:embed audio/sounds/*.mp3
var soundsAudio embed.FS

var (
	fastMode      bool
	minAmplitude  float64
	cooldownMs    int
	repoPath      string
	dryRun        bool
	tapThreshold  float64
	tapArmWindowS int
)

// sensorReady is closed once the sensor worker is about to enter the CFRunLoop.
var sensorReady = make(chan struct{})

// sensorErr receives any fatal error from the sensor worker.
var sensorErr = make(chan error, 1)

const (
	// defaultMinAmplitude ignores anything below this — casual contact,
	// resting your hand on the laptop, etc.
	defaultMinAmplitude = 0.12

	defaultCooldownMs = 750

	// defaultTapThreshold separates a light intentional tap from a hard slap.
	defaultTapThreshold = 0.35

	// defaultTapArmWindowSec is how many seconds after a tap during which a
	// slap is treated as "tap-then-slap" (full add+commit+push pipeline).
	// After this window expires, a slap alone just pushes.
	defaultTapArmWindowSec = 5

	defaultSensorPollInterval = 10 * time.Millisecond
	defaultMaxSampleBatch     = 200
	sensorStartupDelay        = 100 * time.Millisecond
)

type runtimeTuning struct {
	minAmplitude   float64
	tapThreshold   float64
	tapArmWindow   time.Duration
	cooldown       time.Duration
	pollInterval   time.Duration
	maxBatch       int
}

func defaultTuning() runtimeTuning {
	return runtimeTuning{
		minAmplitude: defaultMinAmplitude,
		tapThreshold: defaultTapThreshold,
		tapArmWindow: defaultTapArmWindowSec * time.Second,
		cooldown:     time.Duration(defaultCooldownMs) * time.Millisecond,
		pollInterval: defaultSensorPollInterval,
		maxBatch:     defaultMaxSampleBatch,
	}
}

func applyFastOverlay(base runtimeTuning) runtimeTuning {
	base.pollInterval = 4 * time.Millisecond
	base.cooldown = 350 * time.Millisecond
	if base.minAmplitude > 0.18 {
		base.minAmplitude = 0.18
	}
	if base.maxBatch < 320 {
		base.maxBatch = 320
	}
	return base
}

func main() {
	cmd := &cobra.Command{
		Use:   "gitslap",
		Short: "Git automation via physical gestures on Apple Silicon",
		Long: `gitslap listens to the Apple Silicon accelerometer and maps physical
gestures to git commands:

  tap                → git add .  (also arms a 5s window)
  slap after tap     → git add . + git commit + git push  (full pipeline)
  slap alone         → git push origin HEAD  (no new commit)

The tap arms a window. If you slap within that window, it runs the full
pipeline. A slap with no prior tap just pushes what is already committed.

Replace audio/sounds/01.mp3, 02.mp3, 03.mp3 with your own sounds.
Requires sudo for IOKit HID accelerometer access.`,
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			tuning := defaultTuning()
			if fastMode {
				tuning = applyFastOverlay(tuning)
			}
			if cmd.Flags().Changed("min-amplitude") {
				tuning.minAmplitude = minAmplitude
			}
			if cmd.Flags().Changed("cooldown") {
				tuning.cooldown = time.Duration(cooldownMs) * time.Millisecond
			}
			if cmd.Flags().Changed("tap-threshold") {
				tuning.tapThreshold = tapThreshold
			}
			if cmd.Flags().Changed("tap-arm-window") {
				tuning.tapArmWindow = time.Duration(tapArmWindowS) * time.Second
			}
			return run(cmd.Context(), tuning)
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVar(&fastMode, "fast", false, "Faster detection: shorter cooldown and higher sensitivity")
	cmd.Flags().Float64Var(&minAmplitude, "min-amplitude", defaultMinAmplitude, "Minimum amplitude to register any gesture — below this is ignored (0.0–1.0)")
	cmd.Flags().IntVar(&cooldownMs, "cooldown", defaultCooldownMs, "Cooldown between gesture responses in milliseconds")
	cmd.Flags().StringVar(&repoPath, "repo", "", "Path to git repo (defaults to current working directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print git commands without executing them")
	cmd.Flags().Float64Var(&tapThreshold, "tap-threshold", defaultTapThreshold, "Amplitude below this is a tap; at or above is a slap (0.0–1.0)")
	cmd.Flags().IntVar(&tapArmWindowS, "tap-arm-window", defaultTapArmWindowSec, "Seconds after a tap during which a slap triggers the full add+commit+push pipeline")

	if err := fang.Execute(context.Background(), cmd); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, tuning runtimeTuning) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("gitslap requires root privileges for accelerometer access, run with: sudo gitslap")
	}
	if tuning.minAmplitude < 0 || tuning.minAmplitude > 1 {
		return fmt.Errorf("--min-amplitude must be between 0.0 and 1.0")
	}
	if tuning.tapThreshold < 0 || tuning.tapThreshold > 1 {
		return fmt.Errorf("--tap-threshold must be between 0.0 and 1.0")
	}
	if tuning.cooldown <= 0 {
		return fmt.Errorf("--cooldown must be greater than 0")
	}

	repo := repoPath
	if repo == "" {
		var err error
		repo, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}
	}

	sounds, err := loadSounds()
	if err != nil {
		return fmt.Errorf("loading sounds: %w", err)
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	accelRing, err := shm.CreateRing(shm.NameAccel)
	if err != nil {
		return fmt.Errorf("creating accel shm: %w", err)
	}
	defer accelRing.Close()
	defer accelRing.Unlink()

	go func() {
		close(sensorReady)
		if err := sensor.Run(sensor.Config{
			AccelRing: accelRing,
			Restarts:  0,
		}); err != nil {
			sensorErr <- err
		}
	}()

	select {
	case <-sensorReady:
	case err := <-sensorErr:
		return fmt.Errorf("sensor worker failed: %w", err)
	case <-ctx.Done():
		return nil
	}

	time.Sleep(sensorStartupDelay)

	return listenForGestures(ctx, sounds, accelRing, tuning, repo)
}

// soundFiles holds the embedded sound file paths and FS reference.
type soundFiles struct {
	paths []string
	fs    embed.FS
}

func loadSounds() (*soundFiles, error) {
	entries, err := soundsAudio.ReadDir("audio/sounds")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			paths = append(paths, "audio/sounds/"+e.Name())
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no audio files found in audio/sounds/")
	}
	return &soundFiles{paths: paths, fs: soundsAudio}, nil
}

var speakerMu sync.Mutex

func (sf *soundFiles) playRandom(speakerInit *bool) {
	path := sf.paths[rand.Intn(len(sf.paths))]
	data, err := sf.fs.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gitslap: read %s: %v\n", path, err)
		return
	}
	streamer, format, err := mp3.Decode(io.NopCloser(bytes.NewReader(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gitslap: decode %s: %v\n", path, err)
		return
	}
	defer streamer.Close()

	speakerMu.Lock()
	if !*speakerInit {
		speaker.Init(format.SampleRate, format.SampleRate.N(time.Second/10))
		*speakerInit = true
	}
	speakerMu.Unlock()

	done := make(chan bool)
	speaker.Play(beep.Seq(streamer, beep.Callback(func() {
		done <- true
	})))
	<-done
}

func listenForGestures(ctx context.Context, sounds *soundFiles, accelRing *shm.RingBuffer, tuning runtimeTuning, repo string) error {
	speakerInit := false
	det := detector.New()
	var lastAccelTotal uint64
	var lastEventTime time.Time
	var lastActionTime time.Time

	// lastTapTime tracks when the most recent tap fired.
	// A zero value means no tap has occurred yet (or the window has expired).
	var lastTapTime time.Time

	presetLabel := "default"
	if fastMode {
		presetLabel = "fast"
	}

	fmt.Printf("gitslap: git automation active (repo: %s, tuning: %s)\n", repo, presetLabel)
	if dryRun {
		fmt.Println("gitslap: DRY RUN — git commands will be printed, not executed")
	}
	fmt.Printf("  tap-threshold: %.3f  |  tap-arm window: %ds  |  cooldown: %dms\n",
		tuning.tapThreshold, int(tuning.tapArmWindow.Seconds()), int(tuning.cooldown.Milliseconds()))
	fmt.Println("  tap              → git add .  (arms a window for the next slap)")
	fmt.Println("  slap after tap   → git add . + commit + push")
	fmt.Println("  slap alone       → git push")
	fmt.Println("  ctrl+c to quit")

	ticker := time.NewTicker(tuning.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nbye!")
			return nil
		case err := <-sensorErr:
			return fmt.Errorf("sensor worker failed: %w", err)
		case <-ticker.C:
		}

		now := time.Now()
		tNow := float64(now.UnixNano()) / 1e9

		samples, newTotal := accelRing.ReadNew(lastAccelTotal, shm.AccelScale)
		lastAccelTotal = newTotal
		if len(samples) > tuning.maxBatch {
			samples = samples[len(samples)-tuning.maxBatch:]
		}

		nSamples := len(samples)
		for idx, sample := range samples {
			tSample := tNow - float64(nSamples-idx-1)/float64(det.FS)
			det.Process(sample.X, sample.Y, sample.Z, tSample)
		}

		if len(det.Events) == 0 {
			continue
		}

		ev := det.Events[len(det.Events)-1]
		if ev.Time.Equal(lastEventTime) {
			continue
		}
		lastEventTime = ev.Time

		if time.Since(lastActionTime) <= tuning.cooldown {
			continue
		}
		if ev.Amplitude < tuning.minAmplitude {
			continue
		}

		lastActionTime = now

		if ev.Amplitude < tuning.tapThreshold {
			// Light tap → stage and arm the window
			lastTapTime = now
			fmt.Printf("[tap    amp=%.3fg] → git add .  (slap within %ds to commit+push)\n",
				ev.Amplitude, int(tuning.tapArmWindow.Seconds()))
			go func() {
				if err := gitStage(repo); err != nil {
					fmt.Fprintf(os.Stderr, "gitslap: git add: %v\n", err)
				}
				sounds.playRandom(&speakerInit)
			}()
		} else {
			// Hard slap — check if a tap armed the window
			tapArmed := !lastTapTime.IsZero() && now.Sub(lastTapTime) <= tuning.tapArmWindow
			lastTapTime = time.Time{} // consume the arm regardless

			if tapArmed {
				fmt.Printf("[slap   amp=%.3fg] → git add . + commit + push\n", ev.Amplitude)
				go func() {
					if err := gitStageCommitAndPush(repo); err != nil {
						fmt.Fprintf(os.Stderr, "gitslap: pipeline: %v\n", err)
					}
					sounds.playRandom(&speakerInit)
				}()
			} else {
				fmt.Printf("[slap   amp=%.3fg] → git push\n", ev.Amplitude)
				go func() {
					if err := gitPush(repo); err != nil {
						fmt.Fprintf(os.Stderr, "gitslap: git push: %v\n", err)
					}
					sounds.playRandom(&speakerInit)
				}()
			}
		}
	}
}

// gitStage runs `git add .` in the repo directory.
func gitStage(repo string) error {
	return runGit(repo, "add", ".")
}

// gitStageCommitAndPush runs git add, then commits with an auto message,
// then pushes to origin HEAD. Used when a tap-then-slap sequence is detected.
func gitStageCommitAndPush(repo string) error {
	if err := gitStage(repo); err != nil {
		return err
	}
	msg, err := autoCommitMessage(repo)
	if err != nil {
		msg = "auto: wip"
	}
	if err := runGit(repo, "commit", "-m", msg); err != nil {
		return err
	}
	return gitPush(repo)
}

// gitPush runs `git push origin HEAD` in the repo directory.
func gitPush(repo string) error {
	return runGit(repo, "push", "origin", "HEAD")
}

// runGitOutput is the writer used for dry-run output; overrideable in tests.
var runGitOutput io.Writer = os.Stdout

// autoCommitMessage builds a message from `git diff --cached --name-only`.
// Falls back to "auto: wip" if nothing is staged or the command fails.
func autoCommitMessage(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "diff", "--cached", "--name-only").Output()
	if err != nil {
		return "", err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return buildCommitMessage(files), nil
}

// buildCommitMessage formats staged file names into a commit message string.
func buildCommitMessage(files []string) string {
	if len(files) == 0 {
		return "auto: wip"
	}
	if len(files) > 5 {
		return fmt.Sprintf("auto: %s and %d more", strings.Join(files[:5], ", "), len(files)-5)
	}
	return "auto: " + strings.Join(files, ", ")
}

// runGit executes a git command in the given repo directory.
// In dry-run mode it prints the command instead of executing it.
func runGit(repo string, args ...string) error {
	fullArgs := append([]string{"-C", repo}, args...)
	if dryRun {
		fmt.Fprintf(runGitOutput, "  [dry-run] git %s\n", strings.Join(fullArgs, " "))
		return nil
	}
	cmd := exec.Command("git", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

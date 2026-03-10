# GitSlap

Git automation via physical gestures on your MacBook.

Hit your Apple Silicon MacBook to trigger git commands. Uses the built-in accelerometer to detect taps and slaps, then runs the corresponding git operation and plays a sound.

## Gesture Map

| Gesture | Action |
|---|---|
| Tap (light hit) | `git add .` — stages everything, arms a 5s window |
| Slap within 5s of a tap | `git add . && git commit -m "auto: ..." && git push` |
| Slap with no prior tap | `git push origin HEAD` |

Commit messages are auto-generated from staged filenames, e.g. `auto: main.go, README.md`.

## Requirements

- macOS on Apple Silicon (M2+)
- `sudo` (for IOKit HID accelerometer access)
- Go 1.26+ (to build from source)

## Install

```bash
go install github.com/vineetagarwal/gitslap@latest
sudo cp "$(go env GOPATH)/bin/gitslap" /usr/local/bin/gitslap
```

## Usage

```bash
# Run against the current directory's git repo
sudo gitslap

# Run against a specific repo
sudo gitslap --repo /path/to/your/repo

# Preview what git commands would run without executing them
sudo gitslap --dry-run --repo /path/to/repo
```

## Custom Sounds

Replace the three placeholder MP3 files with your own before building:

```
audio/sounds/01.mp3
audio/sounds/02.mp3
audio/sounds/03.mp3
```

One is picked at random on each gesture.

## Tuning

| Flag | Default | Description |
|---|---|---|
| `--min-amplitude` | `0.12` | Minimum g-force to register any gesture |
| `--tap-threshold` | `0.35` | Below this = tap, at/above = slap |
| `--tap-arm-window` | `5` | Seconds after a tap during which a slap triggers the full pipeline |
| `--cooldown` | `750` | Minimum ms between gesture responses |
| `--fast` | off | 4ms polling, 350ms cooldown, higher sensitivity |

Use `--dry-run` to calibrate thresholds without running real git commands.

## How it works

1. Reads raw accelerometer data via IOKit HID (Apple SPU sensor)
2. Runs vibration detection to identify impacts
3. Classifies by amplitude: below `--tap-threshold` is a tap, above is a slap
4. Checks if a tap recently armed the window to decide which action to run
5. Executes the git command and plays a random sound from `audio/sounds/`

## License

MIT — Vineet Agarwal

---

Inspired by [spank](https://github.com/taigrr/spank) by taigrr. Accelerometer access via [apple-silicon-accelerometer](https://github.com/taigrr/apple-silicon-accelerometer).

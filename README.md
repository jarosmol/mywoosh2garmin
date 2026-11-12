# MyWhoosh2Garmin

A single-executable GUI app that syncs your [MyWhoosh](https://www.mywhoosh.com/) indoor cycling activities to [Garmin Connect](https://connect.garmin.com/) — with full training effect, VO2max, and performance stats support.

No need to run this on the same PC as MyWhoosh — the app downloads activities directly from your MyWhoosh account.


## Why?

MyWhoosh exports FIT files, but they have issues that prevent Garmin from fully processing them:

- **Missing averages** — average power, heart rate, and cadence are not set in the session data
- **Bogus temperature** — every record contains a fake temperature reading
- **Unknown device** — Garmin ignores training effect and VO2max from unknown manufacturers

MyWhoosh2Garmin fixes all of this automatically:

| Problem | Fix |
|---|---|
| Missing avg power / HR / cadence | Calculated from ride records |
| Fake temperature data | Stripped from all records |
| MyWhoosh device identity | Spoofed to a Garmin device you choose (default Fenix 6S) |

The result: your indoor rides show up on Garmin Connect just like a native Garmin recording, complete with **Training Effect**, **VO2max updates**, **Training Load**, and **Training Status**.

## What's new in this fork

- **Garmin MFA / two-step verification** — if Garmin asks for a code, the app shows a dialog for it.
- **Tacx Training app identity (default, recommended)** — rides are uploaded as recorded by Garmin's own Tacx Training app. Garmin Connect then computes Training Effect, Training Load **and cycling VO2max** for your Garmin devices. (Tested: a relabelled MyWhoosh ride updated cycling VO2max. Rides labelled as a physical device — especially with your own Unit ID — did not.)
- **Or choose a Garmin device** rides are labelled as (Fenix, Forerunner, Edge, Epix).
- **Use your own Garmin device** — click *Use my device from a .fit file…* and pick a ride recorded on your watch/Edge (Garmin Connect web → activity → ⚙ → Export Original). The model, **Unit ID** and firmware are copied, so Garmin Connect attributes the rides to that device and shows them as that device's rides. Note: Garmin then treats them as the device's own recordings and does not update VO2max — use the Tacx option for that. The Unit ID can also be typed in by hand.
- Without a Unit ID, a **random serial number** is generated once per installation. (The original tool used one fixed serial for everyone, which Garmin Connect matched to an unrelated Forerunner 255.)
- **Re-upload** button on already-synced rides (delete the ride in Garmin Connect first).
- **No double counting** — rides are skipped if Garmin Connect already has an activity at the same time (e.g. you also recorded on your watch). Can be disabled with `"skip_overlap_check": true` in `config.json`.
- **Background mode** — `mywhoosh2garmin.exe --sync` uploads all new rides without opening a window; output goes to `~/.mywhoosh2garmin/sync.log`.
- **Automatic sync button** (Windows) — creates a Task Scheduler task that runs the background sync every 60 minutes while you are logged in. A message box appears (at most once a day) only if you need to log in again.
- **Optional remembered MyWhoosh password**, encrypted with Windows DPAPI (readable only by your Windows account on that PC), so background sync can renew the MyWhoosh session by itself. Garmin passwords are never stored.
- Rides are uploaded **oldest first**, so Garmin's training status builds up in order.
- The build workflow also runs on pushes to `main` and can be started by hand (Actions → Build & Release → Run workflow); the `.exe` is attached to the run as an artifact.

## Download

Grab the latest release for your platform from the [**Releases**](../../releases) page:

| Platform | File |
|---|---|
| Windows | `mywhoosh2garmin-windows-amd64.exe` |
| Linux | `mywhoosh2garmin-linux-amd64` |

No installation needed — just download and run.

## How to Use

### 1. Enter MyWhoosh credentials

Enter your **MyWhoosh email** and **password**. These are used to log in to the MyWhoosh API and fetch your activity list.

After the first login, the session token is cached locally (`~/.mywhoosh2garmin/`) so you won't need to enter your password again unless the token expires.

### 2. Enter Garmin credentials

Enter your **Garmin Connect email** and **password**. These are only sent directly to Garmin's SSO servers — never stored or sent anywhere else.

After the first login, a session token is cached locally (`~/.mywhoosh2garmin/`) and reused for up to a year. You won't need to enter your password again unless the token expires.

### 3. Fetch Activities

Click **📋 Fetch Activities (last 10 days)** and the app will:

1. Log in to your MyWhoosh account (or resume a cached session)
2. Fetch your activities from the last 10 days
3. Display them in a list showing date, title, distance, duration, power, and heart rate — with an upload button next to each one

### 4. Upload to Garmin

You can either:

- Click **⬆ Upload** next to individual activities to upload them one by one
- Click **⬆ Upload All to Garmin** to upload all unsynced activities at once

For each activity, the app will:

1. Download the FIT file from MyWhoosh
2. Fix averages, strip temperature, spoof device identity
3. Upload to Garmin Connect
4. Mark the activity as synced so it won't be uploaded again

## Building from Source

### Prerequisites

- Go 1.24+
- GCC (for Fyne/CGO)
- Windows cross-compile from Linux: `mingw-w64`, `libgl-dev`, `xorg-dev`, `libxxf86vm-dev`

### Build

```bash
# Linux
go build -o mywhoosh2garmin .

# Both platforms (requires mingw-w64)
./build.sh
# Output in dist/
```

### Run tests

```bash
go test ./...
```

## How It Works

```
  ┌─────────────────────┐
  │  MyWhoosh Web API   │
  │  Login + List       │
  │  Download FIT file  │
  └─────────┬───────────┘
            │
            ▼
  ┌─────────────────────┐
  │  Decode FIT (V2)    │
  │  Fix session avgs   │
  │  Strip temperature  │
  │  Spoof → Fenix 6S   │
  │  Encode FIT (V2)    │
  └─────────┬───────────┘
            │
            ▼
  ┌─────────────────────┐
  │  Garmin SSO Login   │
  │  OAuth1 → OAuth2    │
  │  Upload FIT file    │
  └─────────────────────┘
            │
            ▼
     Garmin Connect
  (Training Effect ✓)
  (VO2max ✓)
  (Training Load ✓)
```

## Credits

- [garth](https://github.com/matin/garth) by matin — Garmin SSO authentication reference
- [muktihari/fit](https://github.com/muktihari/fit) — FIT SDK for Go
- [Fyne](https://fyne.io/) — cross-platform GUI toolkit

## License

GPLv3 — see [LICENSE](LICENSE).

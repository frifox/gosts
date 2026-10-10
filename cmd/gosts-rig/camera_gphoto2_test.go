package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGphoto2 is a gphoto2 stand-in: it finds one camera, and its shell
// fires (trigger-capture) and, when waited on, "downloads" a JPEG for each
// photo fired (and reports its RAW kept on the card), like the A6600.
const fakeGphoto2 = `#!/bin/sh
case "$*" in
*--auto-detect*)
  echo "Model                          Port"
  echo "----------------------------------------------------------"
  echo "Fake Camera (PC Control)       usb:001,002"
  exit 0;;
esac
n=0; fired=0
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  trigger-capture) sleep 0.05; fired=$((fired+1));;
  wait-event-and-download*)
    sleep 0.05
    while [ $n -lt $fired ]; do
      n=$((n+1)); name=$(printf 'capt_DSC%05d' $n)
      printf 'JPEG %d' $n > "$name.JPG"
      echo "Saving file as $name.JPG"
      echo "FILEADDED $name.ARW /"
    done;;
  exit|quit) exit 0;;
  esac
  prompt
done
`

func TestGphoto2Camera(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d string) { photoDir = d }(photoDir)
	photoDir = t.TempDir()

	cams := listCameras()
	if len(cams) != 2 || cams[1].ID != gphoto2Prefix+"usb:001,002" {
		t.Fatalf("cameras %v", cams)
	}
	cam, model, err := openCamera(cams[1].ID)
	if err != nil || model != "Fake Camera (PC Control)" {
		t.Fatal(model, err)
	}
	defer cam.Close()

	// Three photos fired back to back (Shoot returns once fired); their
	// pictures come over afterwards, each to its own photo, in order.
	var mu sync.Mutex
	got := map[int]string{}
	for i := 1; i <= 3; i++ {
		i := i
		if err := cam.Shoot(context.Background(), shutter{firing: func() {}, folder: batchFolder, got: func(p photo, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				got[i] = "error: " + err.Error()
				return
			}
			got[i] = string(p.JPEG)
		}}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i <= 3; i++ {
		if want := "JPEG " + string(rune('0'+i)); got[i] != want {
			t.Errorf("photo %d: %q, want %q", i, got[i], want)
		}
	}
	saved, _ := filepath.Glob(filepath.Join(photoDir, "*", "*.JPG"))
	if len(saved) != 3 {
		t.Errorf("saved %v, want 3 JPEGs", saved)
	}
	if d := cam.(pacedCamera).MinInterval(); d <= 0 || d > 2*time.Second {
		t.Errorf("MinInterval %v", d)
	}
}

// Each batch (from the start, or Reset) saves to a folder of its own, named
// after when its first photo was taken; a photo still coming from before
// the Reset is dropped.
func TestPhotoBatches(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d string) { photoDir = d }(photoDir)
	photoDir = t.TempDir()
	newBatch()
	defer newBatch()

	var mu sync.Mutex
	var got []photoMsg
	c := &cameraConn{out: func(m any) {
		if p, ok := m.(photoMsg); ok {
			mu.Lock()
			got = append(got, p)
			mu.Unlock()
		}
	}}
	if err := c.connect(gphoto2Prefix + "usb:001,002"); err != nil {
		t.Fatal(err)
	}
	defer c.disconnect()
	wait := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			k := len(got)
			mu.Unlock()
			if k >= n || time.Now().After(deadline) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	c.shoot(context.Background(), true, nil)
	wait(1)
	time.Sleep(1100 * time.Millisecond) // the folder names count seconds
	c.reset()
	c.shoot(context.Background(), true, nil)
	wait(2)
	dirs, _ := os.ReadDir(photoDir)
	if len(dirs) != 2 {
		t.Fatalf("folders %v, want one per batch", dirs)
	}
	for _, d := range dirs {
		if _, err := time.Parse(batchName, d.Name()); err != nil {
			t.Errorf("folder %q isn't named after a time", d.Name())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0].N != 1 || got[1].N != 1 {
		t.Fatalf("photos %d and %d: numbering should restart after Reset", got[0].N, got[1].N)
	}
}

// fakeLateGphoto2 is a camera that has a leftover JPEG from before when the
// session starts, and hands photo 1's JPEG over only after photo 2 fired
// (photo 1 has given up waiting by then): neither is photo 2's picture.
const fakeLateGphoto2 = `#!/bin/sh
case "$*" in
*--auto-detect*)
  echo "Fake Camera (PC Control)       usb:001,002"; exit 0;;
esac
fired=0; sent=0; left=1
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
emit() { name=$(printf 'capt_DSC%05d' $1); printf 'JPEG %d' $1 > "$name.JPG"; echo "Saving file as $name.JPG"; echo "FILEADDED $name.ARW /"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  trigger-capture) fired=$((fired+1));;
  wait-event-and-download*)
    if [ $left = 1 ]; then emit 90; left=0; fi
    if [ $fired -ge 2 ]; then
      while [ $sent -lt $fired ]; do sent=$((sent+1)); emit $((90+sent)); done
    fi;;
  exit|quit) exit 0;;
  esac
  prompt
done
`

func TestGphoto2Leftovers(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeLateGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d string) { photoDir = d }(photoDir)
	photoDir = t.TempDir()
	newBatch()
	defer newBatch()
	defer func(w, c time.Duration) { pictureWait, confirmWait = w, c }(pictureWait, confirmWait)
	pictureWait, confirmWait = 300*time.Millisecond, 500*time.Millisecond

	cam, _, err := openCamera(gphoto2Prefix + "usb:001,002")
	if err != nil {
		t.Fatal(err)
	}
	defer cam.Close()
	var mu sync.Mutex
	got := map[int]string{}
	shoot := func(i int) {
		if err := cam.Shoot(context.Background(), shutter{firing: func() {}, folder: batchFolder, got: func(p photo, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				got[i] = "gave up"
				return
			}
			got[i] = string(p.JPEG)
		}}); err != nil {
			t.Fatal(err)
		}
	}
	shoot(1)
	time.Sleep(time.Second) // photo 1 gives up waiting
	shoot(2)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	// Photo 1's file only comes after another shutter: photo 2 waits for
	// it, and photo 1 is fired again (92, an extra photo); photo 2 is 93.
	if got[1] != "gave up" || got[2] != "JPEG 93" {
		t.Fatalf("photo 1: %q, photo 2: %q; want gave up, JPEG 93 (not the leftover 90, photo 1's late 91 or its second firing's 92)", got[1], got[2])
	}
	// The leftovers are kept, not lost.
	saved, _ := filepath.Glob(filepath.Join(photoDir, "*", "*.JPG"))
	if len(saved) != 4 {
		t.Errorf("saved %v, want all 4 JPEGs (three of them leftovers)", saved)
	}
}

// Sample shots go in a folder of their own beside the batch's: the same
// name, with "-samples".
func TestSampleFolder(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d string) { photoDir = d }(photoDir)
	photoDir = t.TempDir()
	newBatch()
	defer newBatch()

	var mu sync.Mutex
	n := 0
	c := &cameraConn{out: func(m any) {
		if _, ok := m.(photoMsg); ok {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}}
	if err := c.connect(gphoto2Prefix + "usb:001,002"); err != nil {
		t.Fatal(err)
	}
	defer c.disconnect()
	c.sample(context.Background())
	c.shoot(context.Background(), true, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		k := n
		mu.Unlock()
		if k == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	samples, _ := filepath.Glob(filepath.Join(photoDir, "*-samples", "*.JPG"))
	all, _ := filepath.Glob(filepath.Join(photoDir, "*", "*.JPG"))
	if len(samples) != 1 || len(all) != 2 {
		t.Fatalf("samples %v, all %v: want the sample alone in <batch>-samples", samples, all)
	}
	if want := filepath.Dir(samples[0]); filepath.Base(want) != filepath.Base(batchFolder())+"-samples" {
		t.Errorf("sample folder %q, want the batch's name with -samples", want)
	}
}

// fakeRefusingGphoto2 ignores the first shutter (no file: as an A6600
// busy with the last photo, or in AF-S/DMF finding no focus), then takes
// each one after as the next number from 91.
const fakeRefusingGphoto2 = `#!/bin/sh
case "$*" in
*--auto-detect*)
  echo "Fake Camera (PC Control)       usb:001,002"; exit 0;;
esac
fired=0; sent=0; left=1
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
emit() { name=$(printf 'capt_DSC%05d' $1); printf 'JPEG %d' $1 > "$name.JPG"; echo "Saving file as $name.JPG"; echo "FILEADDED $name.ARW /"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  trigger-capture) fired=$((fired+1));;
  wait-event-and-download*)
    if [ $left = 1 ]; then emit 90; left=0; fi
    while [ $sent -lt $((fired-1)) ]; do sent=$((sent+1)); emit $((90+sent)); done;;
  exit|quit) exit 0;;
  esac
  prompt
done
`

// TestGphoto2Refused: a shutter the camera ignores is fired again before
// the next photo's, so every photo gets its own picture.
func TestGphoto2Refused(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeRefusingGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d string) { photoDir = d }(photoDir)
	photoDir = t.TempDir()
	newBatch()
	defer newBatch()
	defer func(w, h, c time.Duration) { pictureWait, heldWait, confirmWait = w, h, c }(pictureWait, heldWait, confirmWait)
	pictureWait, heldWait, confirmWait = 2*time.Second, 300*time.Millisecond, 300*time.Millisecond

	cam, _, err := openCamera(gphoto2Prefix + "usb:001,002")
	if err != nil {
		t.Fatal(err)
	}
	defer cam.Close()
	var mu sync.Mutex
	got := map[int]string{}
	shoot := func(i int) {
		if err := cam.Shoot(context.Background(), shutter{firing: func() {}, folder: batchFolder, got: func(p photo, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				got[i] = "gave up"
				return
			}
			got[i] = string(p.JPEG)
		}}); err != nil {
			t.Fatal(err)
		}
	}
	shoot(1) // ignored: fired again before photo 2's, and taken then
	shoot(2)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got[1] != "JPEG 91" || got[2] != "JPEG 92" {
		t.Fatalf("photo 1: %q, photo 2: %q; want JPEG 91, JPEG 92 (the shutter fired again for photo 1)", got[1], got[2])
	}
}

// fakePreviewGphoto2 answers capture-preview as gphoto2 does: the frame in
// the shell's folder.
const fakePreviewGphoto2 = `#!/bin/sh
case "$*" in
*--auto-detect*)
  echo "Fake Camera (PC Control)       usb:001,002"; exit 0;;
esac
n=0
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  capture-preview) n=$((n+1)); printf 'FRAME %d' $n > capture_preview.jpg; echo "Saving file as capture_preview.jpg";;
  exit|quit) exit 0;;
  esac
  prompt
done
`

// TestGphoto2Preview: live-view frames come from capture-preview, one after
// another, the file not left behind.
func TestGphoto2Preview(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakePreviewGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cam, _, err := openCamera(gphoto2Prefix + "usb:001,002")
	if err != nil {
		t.Fatal(err)
	}
	defer cam.Close()
	pc, ok := cam.(previewCamera)
	if !ok {
		t.Fatal("no live view")
	}
	for i := 1; i <= 2; i++ {
		jpg, err := pc.Preview()
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("FRAME %d", i); string(jpg) != want {
			t.Errorf("frame %d: %q, want %q", i, jpg, want)
		}
	}
	if _, err := os.Stat(filepath.Join(cam.(*gphoto2Camera).tmp, "capture_preview.jpg")); err == nil {
		t.Error("the frame's file was left behind")
	}
}

// fakeHangingGphoto2 answers "hang" only after a while (a camera not
// answering), anything else at once with "out:" and the line.
const fakeHangingGphoto2 = `#!/bin/sh
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  hang) sleep 1;;
  exit|quit) exit 0;;
  *) echo "out:$line";;
  esac
  prompt
done
`

// TestGphoto2Stuck: a command the camera doesn't answer in time leaves the
// shell stuck: the next fail at once (not each waiting its time), till the
// late answer comes; it's dropped, and commands answer as before.
func TestGphoto2Stuck(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeHangingGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d time.Duration) { stuckRetry = d }(stuckRetry)
	stuckRetry = 100 * time.Millisecond
	sh, err := startGphoto2Shell("usb:001,002", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sh.close()
	if _, err := sh.run("hang", 200*time.Millisecond); !errors.Is(err, errCameraStuck) {
		t.Fatalf("hang: %v", err)
	}
	start := time.Now()
	if _, err := sh.run("a", 5*time.Second); !errors.Is(err, errCameraStuck) {
		t.Fatalf("while stuck: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v while stuck", d)
	}
	time.Sleep(time.Second) // the late answer comes
	out, err := sh.run("b", 5*time.Second)
	if err != nil || !strings.Contains(out, "out:b") || strings.Contains(out, "hang") {
		t.Fatalf("after: %q, %v", out, err)
	}
}

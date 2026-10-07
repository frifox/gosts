package main

import (
	"context"
	"os"
	"path/filepath"
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
	defer func(w time.Duration) { pictureWait = w }(pictureWait)
	pictureWait = 300 * time.Millisecond

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
	if got[1] != "gave up" || got[2] != "JPEG 92" {
		t.Fatalf("photo 1: %q, photo 2: %q; want gave up, JPEG 92 (not the leftover 90 or photo 1's late 91)", got[1], got[2])
	}
	// The leftovers are kept, not lost.
	saved, _ := filepath.Glob(filepath.Join(photoDir, "*", "*.JPG"))
	if len(saved) != 3 {
		t.Errorf("saved %v, want all 3 JPEGs (two of them leftovers)", saved)
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

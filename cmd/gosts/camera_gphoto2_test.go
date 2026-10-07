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
		if err := cam.Shoot(context.Background(), func() {}, func(p photo, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				got[i] = "error: " + err.Error()
				return
			}
			got[i] = string(p.JPEG)
		}); err != nil {
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
		if _, err := time.Parse("2006-01-02 15:04:05", d.Name()); err != nil {
			t.Errorf("folder %q isn't named after a time", d.Name())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0].N != 1 || got[1].N != 1 {
		t.Fatalf("photos %d and %d: numbering should restart after Reset", got[0].N, got[1].N)
	}
}

package main

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestCamPose: the camera looks at the tilt axis, level at elevation 0 and
// above it looking down at 30°; azimuth turns it round the object (Z up);
// its axes are at right angles.
func TestCamPose(t *testing.T) {
	r := defaultConfig().Rig
	r.CameraZ = 0
	axis := vec3{0, 0, 20 + r.PostZ + 30 - r.TurntableZ} // the tilt axis' middle, from the turntable's top
	for _, e := range []float64{0, 30, -20} {
		for _, az := range []float64{0, 90, -135} {
			c := camPose(r, e, az)
			to := axis.sub(c.Pos)
			to = to.scale(1 / math.Sqrt(to.dot(to)))
			if ang := math.Acos(math.Min(1, to.dot(c.Fwd))) * 180 / math.Pi; ang > 12 {
				t.Errorf("e %v az %v: looking %.1f° off the tilt axis", e, az, ang)
			}
			if math.Abs(c.Fwd.dot(c.Up)) > 1e-9 || math.Abs(c.Fwd.dot(c.Right)) > 1e-9 || math.Abs(c.Up.dot(c.Right)) > 1e-9 {
				t.Errorf("e %v az %v: axes not at right angles", e, az)
			}
			// Elevation: up, and looking down at it.
			if want := math.Sin(-e * math.Pi / 180); math.Abs(c.Fwd.Z-want) > 1e-9 {
				t.Errorf("e %v: looking %.3f down/up, want %.3f", e, c.Fwd.Z, want)
			}
		}
	}
	// Azimuth turns the camera round the object: 90° apart.
	a, b := camPose(r, 0, 0), camPose(r, 0, 90)
	ha, hb := vec3{a.Pos.X, a.Pos.Y, 0}, vec3{b.Pos.X, b.Pos.Y, 0}
	if cos := ha.dot(hb) / math.Sqrt(ha.dot(ha)*hb.dot(hb)); math.Abs(cos) > 0.1 {
		t.Errorf("azimuth 0 and 90: %.0f° apart round the object", math.Acos(cos)*180/math.Pi)
	}
}

// TestWriteExport: each export writes its files, the poses sound.
func TestWriteExport(t *testing.T) {
	dir := t.TempDir()
	r := defaultConfig().Rig
	var photos []exportPhoto
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, "capt_DSC0000"+string(rune('1'+i))+".JPG")
		f, _ := os.Create(path)
		jpeg.Encode(f, image.NewGray(image.Rect(0, 0, 60, 40)), nil)
		f.Close()
		photos = append(photos, exportPhoto{Path: path, Pose: camPose(r, float64(i*10), float64(i*120))})
	}
	for _, kind := range exportKinds {
		if err := writeExport(kind, dir, photos, exportIntrinsics{FocalMM: 18}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	for _, name := range []string{"alignment-metashape.csv", "capt_DSC00001.xmp", "transforms.json", "colmap/images.txt", "colmap/cameras.txt", "colmap/points3D.txt", "gravity.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	var tj struct {
		Frames []struct {
			M [4][4]float64 `json:"transform_matrix"`
		} `json:"frames"`
	}
	data, _ := os.ReadFile(filepath.Join(dir, "transforms.json"))
	if err := json.Unmarshal(data, &tj); err != nil || len(tj.Frames) != 3 {
		t.Fatalf("transforms.json: %v, %d frames", err, len(tj.Frames))
	}
	// The camera's position in metres, as in the pose.
	if m := tj.Frames[0].M; math.Abs(m[0][3]-photos[0].Pose.Pos.X/1000) > 1e-9 || math.Abs(m[2][3]-photos[0].Pose.Pos.Z/1000) > 1e-9 {
		t.Errorf("transforms.json position %v %v, pose %v", m[0][3], m[2][3], photos[0].Pose.Pos)
	}
	// A unit quaternion of a proper rotation.
	c := photos[1].Pose
	qw, qx, qy, qz := quaternion([3]vec3{c.Right, c.Up.scale(-1), c.Fwd})
	if n := qw*qw + qx*qx + qy*qy + qz*qz; math.Abs(n-1) > 1e-9 {
		t.Errorf("quaternion norm %v", n)
	}
}

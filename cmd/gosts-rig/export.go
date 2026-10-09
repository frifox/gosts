package main

// Alignment data for photogrammetry apps: where each photo was taken from,
// worked out from the rig's geometry (Rig Setup) and the pose the rig was
// really at when its shutter went. The turntable turns the object, not the
// camera; for the apps it's the camera going round a still object (each
// pose turned back by its azimuth), which holds with a plain backdrop.
// The poses are priors, a few degrees out (the arm trails its path a
// little; the measurements and the camera's mounting are approximate): the
// apps refine them.

import (
	"encoding/json"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// The exports (Plan.Export).
const (
	ExportMetashape   = "metashape"   // Agisoft Metashape: camera positions (CSV, Import Reference)
	ExportRealityScan = "realityscan" // RealityScan / RealityCapture: an XMP beside each photo (pose prior)
	ExportNerfstudio  = "nerfstudio"  // Nerfstudio, Gaussian splatting, instant-ngp: transforms.json
	ExportColmap      = "colmap"      // COLMAP: a text model (cameras, images; points empty)
	ExportApple       = "apple"       // Apple Object Capture: gravity per photo (JSON, for a companion tool)
)

// exportKinds are the exports there are.
var exportKinds = []string{ExportMetashape, ExportRealityScan, ExportNerfstudio, ExportColmap, ExportApple}

// The A6600's sensor (APS-C), for the focal length in pixels.
const sensorW, sensorH = 23.5, 15.6 // mm

// poseAccuracy is how far out (mm, degrees) the poses may be, as the apps
// are told.
const poseAccuracyMM, poseAccuracyDeg = 15.0, 3.0

// vec3 is a point or direction, in mm.
type vec3 struct{ X, Y, Z float64 }

func (a vec3) sub(b vec3) vec3      { return vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a vec3) scale(k float64) vec3 { return vec3{a.X * k, a.Y * k, a.Z * k} }
func (a vec3) dot(b vec3) float64   { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a vec3) cross(b vec3) vec3 {
	return vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}

// cameraPose is a photo's camera in the object's frame: right-handed, Z up,
// mm, the origin the middle of the turntable's top, X towards the camera's
// side at azimuth 0 turned back (the object's own frame: see camPose).
type cameraPose struct {
	Pos             vec3 // the sensor
	Fwd, Up, Right  vec3 // where the lens looks, the camera's up and right (unit)
	Elevation, Azim float64
}

// camPose is the pose for a photo taken at elevation e, azimuth az (the
// rig's angles), from the rig's measurements: as the 3D view draws it (Y up
// there): the swing tilts about the servos' axis, P + PostZ + 30 mm up; the
// camera sits on its -X bar (CameraOffset along the arms, CameraZ above
// them), its sensor 2 mm in front of the body's middle (the lens 10 mm
// aside), looking along the arms at the axis.
func camPose(r Rig, e, az float64) cameraPose {
	const p = 20.0 // the profile
	pivot := p + r.PostZ + 30
	barX := r.SwingX/2 - p/2
	s := vec3{-barX + r.CameraOffset + 2, p/2 + 34 + r.CameraZ - 2, 10} // the sensor, in the swing's frame
	ce, se := math.Cos(rad(e)), math.Sin(rad(e))
	tilt := func(v vec3) vec3 { return vec3{v.X*ce + v.Y*se, -v.X*se + v.Y*ce, v.Z} } // the swing at elevation e
	pos := tilt(s)
	pos.Y += pivot
	fwd, up := tilt(vec3{1, 0, 0}), tilt(vec3{0, 1, 0})
	// The object turned by az is the camera turned back by az round it (Y).
	ca, sa := math.Cos(rad(-az)), math.Sin(rad(-az))
	turn := func(v vec3) vec3 { return vec3{v.X*ca + v.Z*sa, v.Y, -v.X*sa + v.Z*ca} }
	// Y up → Z up (right-handed: X, −Z, Y), from the turntable's top.
	zup := func(v vec3) vec3 { return vec3{v.X, -v.Z, v.Y} }
	pos = turn(pos)
	pos.Y -= r.TurntableZ
	cp := cameraPose{Pos: zup(pos), Fwd: zup(turn(fwd)), Up: zup(turn(up)), Elevation: e, Azim: az}
	cp.Right = cp.Fwd.cross(cp.Up)
	return cp
}

// exportPhoto is a photo to export: its file and the pose it was taken at.
type exportPhoto struct {
	Path string // the JPEG
	Pose cameraPose
}

// exportIntrinsics are the camera's, as the apps want them.
type exportIntrinsics struct {
	W, H    int     // pixels
	FocalMM float64 // 0: not known
}

// focalPx is the focal length in pixels (0: not known).
func (in exportIntrinsics) focalPx() float64 {
	if in.FocalMM <= 0 || in.W <= 0 {
		return 0
	}
	return in.FocalMM / sensorW * float64(in.W)
}

// writeExport writes the alignment data of kind for the photos into dir.
func writeExport(kind, dir string, photos []exportPhoto, in exportIntrinsics) error {
	if len(photos) == 0 {
		return nil
	}
	if in.W == 0 { // the size from the first photo
		if f, err := os.Open(photos[0].Path); err == nil {
			if c, err := jpeg.DecodeConfig(f); err == nil {
				in.W, in.H = c.Width, c.Height
			}
			f.Close()
		}
	}
	switch kind {
	case ExportMetashape:
		return exportMetashape(dir, photos)
	case ExportRealityScan:
		return exportRealityScan(photos, in)
	case ExportNerfstudio:
		return exportNerfstudio(dir, photos, in)
	case ExportColmap:
		return exportColmap(dir, photos, in)
	case ExportApple:
		return exportApple(dir, photos)
	}
	return fmt.Errorf("no export %q", kind)
}

// Metashape: File → Import → Import Reference, the camera positions (mm,
// local coordinates) with their accuracy; the label is the photo's name.
func exportMetashape(dir string, photos []exportPhoto) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# gosts-rig camera positions: local coordinates, mm, Z up, origin the turntable's middle (Import Reference)\n")
	fmt.Fprintf(&b, "# label,x,y,z,accuracy\n")
	for _, p := range photos {
		fmt.Fprintf(&b, "%s,%.2f,%.2f,%.2f,%.1f\n", filepath.Base(p.Path), p.Pose.Pos.X, p.Pose.Pos.Y, p.Pose.Pos.Z, poseAccuracyMM)
	}
	return os.WriteFile(filepath.Join(dir, "alignment-metashape.csv"), []byte(b.String()), 0o644)
}

// RealityScan / RealityCapture: an XMP beside each photo (same name), the
// pose as a prior ("initial": refined in alignment). Its rotation takes the
// world to the camera (the camera's right, down and forward as rows); the
// position is in metres.
func exportRealityScan(photos []exportPhoto, in exportIntrinsics) error {
	for _, p := range photos {
		c := p.Pose
		down := c.Up.scale(-1)
		rows := [3]vec3{c.Right, down, c.Fwd}
		var rot []string
		for _, r := range rows {
			rot = append(rot, fmt.Sprintf("%.9f %.9f %.9f", r.X, r.Y, r.Z))
		}
		focal35 := ""
		if in.FocalMM > 0 {
			focal35 = fmt.Sprintf(` xcr:FocalLength35mm="%.3f"`, in.FocalMM*36/sensorW)
		}
		xmp := fmt.Sprintf(`<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description xmlns:xcr="http://www.capturingreality.com/ns/xcr/1.1#" xcr:Version="3" xcr:PosePrior="initial" xcr:Coordinates="absolute"%s>
      <xcr:Rotation>%s</xcr:Rotation>
      <xcr:Position>%.6f %.6f %.6f</xcr:Position>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
`, focal35, strings.Join(rot, " "), c.Pos.X/1000, c.Pos.Y/1000, c.Pos.Z/1000)
		name := strings.TrimSuffix(p.Path, filepath.Ext(p.Path)) + ".xmp"
		if err := os.WriteFile(name, []byte(xmp), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Nerfstudio (and Gaussian splatting tools, instant-ngp): transforms.json,
// each frame's camera-to-world matrix in OpenGL's way (the camera looking
// along −Z, Y up), metres.
func exportNerfstudio(dir string, photos []exportPhoto, in exportIntrinsics) error {
	type frame struct {
		FilePath  string       `json:"file_path"`
		Transform [4][4]float64 `json:"transform_matrix"`
	}
	out := map[string]any{"camera_model": "OPENCV"}
	if f := in.focalPx(); f > 0 {
		out["fl_x"], out["fl_y"] = f, f
		out["cx"], out["cy"] = float64(in.W)/2, float64(in.H)/2
		out["w"], out["h"] = in.W, in.H
		out["camera_angle_x"] = 2 * math.Atan(float64(in.W)/(2*f))
	}
	var frames []frame
	for _, p := range photos {
		c := p.Pose
		back := c.Fwd.scale(-1)
		frames = append(frames, frame{FilePath: filepath.Base(p.Path), Transform: [4][4]float64{
			{c.Right.X, c.Up.X, back.X, c.Pos.X / 1000},
			{c.Right.Y, c.Up.Y, back.Y, c.Pos.Y / 1000},
			{c.Right.Z, c.Up.Z, back.Z, c.Pos.Z / 1000},
			{0, 0, 0, 1},
		}})
	}
	out["frames"] = frames
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "transforms.json"), data, 0o644)
}

// COLMAP: a text model in colmap/ (cameras.txt, images.txt, an empty
// points3D.txt): point_triangulator builds the points on these poses, or
// they're a start for mapping. Each image: the rotation taking the world to
// the camera (a quaternion), and the translation, metres.
func exportColmap(dir string, photos []exportPhoto, in exportIntrinsics) error {
	cdir := filepath.Join(dir, "colmap")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return err
	}
	f := in.focalPx()
	if f <= 0 {
		f = float64(max(in.W, 1)) // unknown: about a normal lens
	}
	cams := fmt.Sprintf("# Camera list with one line of data per camera:\n#   CAMERA_ID, MODEL, WIDTH, HEIGHT, PARAMS[]\n1 SIMPLE_RADIAL %d %d %.3f %.3f %.3f 0\n",
		in.W, in.H, f, float64(in.W)/2, float64(in.H)/2)
	var b strings.Builder
	b.WriteString("# Image list with two lines of data per image:\n#   IMAGE_ID, QW, QX, QY, QZ, TX, TY, TZ, CAMERA_ID, NAME\n#   POINTS2D[] as (X, Y, POINT3D_ID)\n")
	for i, p := range photos {
		c := p.Pose
		down := c.Up.scale(-1)
		r := [3]vec3{c.Right, down, c.Fwd} // world → camera, rows
		pos := c.Pos.scale(1.0 / 1000)
		t := vec3{-r[0].dot(pos), -r[1].dot(pos), -r[2].dot(pos)}
		qw, qx, qy, qz := quaternion(r)
		fmt.Fprintf(&b, "%d %.9f %.9f %.9f %.9f %.6f %.6f %.6f 1 %s\n\n", i+1, qw, qx, qy, qz, t.X, t.Y, t.Z, filepath.Base(p.Path))
	}
	for name, data := range map[string]string{"cameras.txt": cams, "images.txt": b.String(), "points3D.txt": "# 3D point list (empty: point_triangulator fills it)\n"} {
		if err := os.WriteFile(filepath.Join(cdir, name), []byte(data), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// quaternion is the unit quaternion (w, x, y, z) of rotation matrix rows r.
func quaternion(r [3]vec3) (w, x, y, z float64) {
	m := [3][3]float64{{r[0].X, r[0].Y, r[0].Z}, {r[1].X, r[1].Y, r[1].Z}, {r[2].X, r[2].Y, r[2].Z}}
	tr := m[0][0] + m[1][1] + m[2][2]
	switch {
	case tr > 0:
		s := math.Sqrt(tr+1) * 2
		return s / 4, (m[2][1] - m[1][2]) / s, (m[0][2] - m[2][0]) / s, (m[1][0] - m[0][1]) / s
	case m[0][0] > m[1][1] && m[0][0] > m[2][2]:
		s := math.Sqrt(1+m[0][0]-m[1][1]-m[2][2]) * 2
		return (m[2][1] - m[1][2]) / s, s / 4, (m[0][1] + m[1][0]) / s, (m[0][2] + m[2][0]) / s
	case m[1][1] > m[2][2]:
		s := math.Sqrt(1+m[1][1]-m[0][0]-m[2][2]) * 2
		return (m[0][2] - m[2][0]) / s, (m[0][1] + m[1][0]) / s, s / 4, (m[1][2] + m[2][1]) / s
	default:
		s := math.Sqrt(1+m[2][2]-m[0][0]-m[1][1]) * 2
		return (m[1][0] - m[0][1]) / s, (m[0][2] + m[2][0]) / s, (m[1][2] + m[2][1]) / s, s / 4
	}
}

// Apple Object Capture (RealityKit's PhotogrammetrySession, which PhotoCatch
// and the like use) takes no camera poses, only, per photo, which way is
// down for the camera (gravity): gravity.json has it in the camera's frame
// (x right, y up, z towards the viewer, as Apple's), for a tool that passes
// it on as each PhotogrammetrySample's gravity.
func exportApple(dir string, photos []exportPhoto) error {
	type entry struct {
		File    string     `json:"file"`
		Gravity [3]float64 `json:"gravity"`
	}
	var out []entry
	down := vec3{0, 0, -1} // in the object's frame (Z up)
	for _, p := range photos {
		c := p.Pose
		g := [3]float64{down.dot(c.Right), down.dot(c.Up), down.dot(c.Fwd.scale(-1))}
		out = append(out, entry{File: filepath.Base(p.Path), Gravity: g})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "gravity.json"), data, 0o644)
}

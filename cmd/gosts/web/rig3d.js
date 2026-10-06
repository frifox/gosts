// Live 3D model of the photogrammetry rig, rendered with three.js.
//
// Units are millimetres. three.js is Y-up: x runs along the base's 600 mm
// sides, y up, z across the base (500 mm). The tilt axis runs along z
// through the two servos on top of the posts; the turntable turns about y.
import * as THREE from "three";
import { OrbitControls } from "./vendor/OrbitControls.js";
import { STLLoader } from "./vendor/STLLoader.js";

const P = 20; // 2020 profile size

// pivotHeight is the tilt axis' height (mm): on top of the posts, through
// the servo horns. The camera orbits it and looks at it.
export const pivotHeight = (d) => P + d.PostZ + 30;

// Default measurements (mm), as in gosts' config: seen from above, X along
// the base sides that carry the posts, Y along the tilt axis, Z up.
export const DEFAULT_RIG = { BaseX: 600, BaseY: 500, PostZ: 400, SwingX: 600, SwingY: 450, CameraOffset: -50, TurntableZ: 400, ObjectZ: 100 };

const rad = (d) => d * Math.PI / 180;

// ---------------------------------------------------------------- materials
const mat = {
  alu: new THREE.MeshStandardMaterial({ color: 0xd5d9de, metalness: 0.3, roughness: 0.42 }), // brushed, no environment map to reflect
  servo: new THREE.MeshStandardMaterial({ color: 0x1d2025, metalness: 0.1, roughness: 0.55 }),
  servoEdge: new THREE.MeshStandardMaterial({ color: 0x2b3038, metalness: 0.2, roughness: 0.5 }),
  horn: new THREE.MeshStandardMaterial({ color: 0xb4bac2, metalness: 0.4, roughness: 0.3 }),
  body: new THREE.MeshStandardMaterial({ color: 0x17191c, metalness: 0.15, roughness: 0.62 }),
  rubber: new THREE.MeshStandardMaterial({ color: 0x0e0f11, metalness: 0, roughness: 0.92 }),
  lens: new THREE.MeshStandardMaterial({ color: 0x141518, metalness: 0.4, roughness: 0.4 }),
  glass: new THREE.MeshStandardMaterial({ color: 0x223044, metalness: 0.9, roughness: 0.08 }),
  flash: new THREE.MeshStandardMaterial({ color: 0xf2f4f7, metalness: 0, roughness: 0.35, emissive: 0x2a2d33 }),
  flashBody: new THREE.MeshStandardMaterial({ color: 0x24272c, metalness: 0.2, roughness: 0.5 }),
  screen: new THREE.MeshStandardMaterial({ color: 0x0b0d12, metalness: 0.6, roughness: 0.15 }),
  table: new THREE.MeshStandardMaterial({ color: 0x2c3138, metalness: 0.3, roughness: 0.5 }),
  object: new THREE.MeshStandardMaterial({ color: 0xd08a2c, metalness: 0.1, roughness: 0.55 }),
  floor: new THREE.MeshStandardMaterial({ color: 0x888888, roughness: 1, metalness: 0 }),
};

// ---------------------------------------------------------------- 2020 extrusion
// Cross-section: 20×20 mm, a T-slot on every side (6.2 mm opening, wider
// behind the lips) and the 4.2 mm centre bore.
const profileShape = (() => {
  // One side, from corner (-10,-10) along y = -10, slot in the middle.
  const side = [[-10, -10], [-3.1, -10], [-3.1, -8.2], [-5.6, -8.2], [-5.6, -6.6], [-2.6, -4.4],
    [2.6, -4.4], [5.6, -6.6], [5.6, -8.2], [3.1, -8.2], [3.1, -10]];
  const pts = [];
  for (let k = 0; k < 4; k++) { // the other sides: rotate by 90°
    const c = Math.cos(k * Math.PI / 2), s = Math.sin(k * Math.PI / 2);
    for (const [x, y] of side) pts.push(new THREE.Vector2(x * c - y * s, x * s + y * c));
  }
  const shape = new THREE.Shape(pts);
  const bore = new THREE.Path();
  bore.absarc(0, 0, 2.1, 0, Math.PI * 2, true);
  shape.holes.push(bore);
  return shape;
})();
const profileCache = new Map();
function profileGeometry(len) {
  if (!profileCache.has(len)) {
    const g = new THREE.ExtrudeGeometry(profileShape, { depth: len, bevelEnabled: false, curveSegments: 6 });
    g.translate(0, 0, -len / 2); // centred on its length
    g.computeVertexNormals();
    profileCache.set(len, g);
  }
  return profileCache.get(len);
}
// profile places a 2020 extrusion from a to b (Vector3s).
function profile(a, b) {
  const dir = new THREE.Vector3().subVectors(b, a);
  const m = new THREE.Mesh(profileGeometry(dir.length()), mat.alu);
  m.position.copy(a).add(b).multiplyScalar(0.5);
  m.quaternion.setFromUnitVectors(new THREE.Vector3(0, 0, 1), dir.normalize());
  m.castShadow = m.receiveShadow = true;
  return m;
}

function box(w, h, d, material, x = 0, y = 0, z = 0, radius = 0) {
  const g = radius ? roundedBox(w, h, d, radius) : new THREE.BoxGeometry(w, h, d);
  const m = new THREE.Mesh(g, material);
  m.position.set(x, y, z);
  m.castShadow = m.receiveShadow = true;
  return m;
}
// roundedBox: a box with rounded vertical edges (extruded rounded rectangle).
function roundedBox(w, h, d, r) {
  const s = new THREE.Shape();
  const x = -w / 2, y = -d / 2;
  s.moveTo(x + r, y);
  s.lineTo(x + w - r, y); s.quadraticCurveTo(x + w, y, x + w, y + r);
  s.lineTo(x + w, y + d - r); s.quadraticCurveTo(x + w, y + d, x + w - r, y + d);
  s.lineTo(x + r, y + d); s.quadraticCurveTo(x, y + d, x, y + d - r);
  s.lineTo(x, y + r); s.quadraticCurveTo(x, y, x + r, y);
  const g = new THREE.ExtrudeGeometry(s, { depth: h, bevelEnabled: true, bevelThickness: r * 0.4, bevelSize: r * 0.4, bevelSegments: 2, curveSegments: 6 });
  g.rotateX(-Math.PI / 2);
  g.translate(0, -h / 2, 0);
  g.computeVertexNormals();
  return g;
}
function cylinder(r, len, material, axis = "y", segs = 40) {
  const g = new THREE.CylinderGeometry(r, r, len, segs);
  if (axis === "x") g.rotateZ(Math.PI / 2);
  if (axis === "z") g.rotateX(Math.PI / 2);
  const m = new THREE.Mesh(g, material);
  m.castShadow = m.receiveShadow = true;
  return m;
}

// ---------------------------------------------------------------- ST3215
// Feetech/Waveshare ST3215: about 45 × 25 × 35 mm, output horn on one face.
// Built with the horn on its -z face, centred on the origin.
function st3215() {
  const g = new THREE.Group();
  const bodyH = 45, bodyW = 25, bodyD = 35;
  g.add(box(bodyW, bodyH, bodyD, mat.servo, 0, -10, 0, 2));
  g.add(box(bodyW + 0.4, 3, bodyD + 0.4, mat.servoEdge, 0, -22, 0)); // case seam
  // Mounting ears.
  for (const sy of [-1, 1]) g.add(box(bodyW, 3, 8, mat.servo, 0, -10 + sy * 18, -bodyD / 2 - 2));
  // Output horn (metal disc with a hub) on the -z face, on the axis.
  const horn = cylinder(11, 3, mat.horn, "z");
  horn.position.set(0, 0, -bodyD / 2 - 2);
  g.add(horn);
  const hub = cylinder(4.5, 4, mat.horn, "z", 24);
  hub.position.set(0, 0, -bodyD / 2 - 4);
  g.add(hub);
  return g;
}

// ---------------------------------------------------------------- Sony A6600
// Rangefinder-style body about 120 × 67 × 69 mm with a deep grip, the EVF on
// the top left, a short zoom and a ring flash round the lens. Built looking
// along +x (lens towards +x), width along z.
function a6600() {
  const g = new THREE.Group();
  const W = 120, H = 67, D = 40; // body shell depth without the grip
  g.add(box(D, H, W, mat.body, 0, 0, 0, 4));
  // Grip on the right (seen from behind: -z), deeper than the body.
  const grip = box(30, H - 4, 34, mat.rubber, 14, -2, -W / 2 + 17, 6);
  g.add(grip);
  // EVF hump on the top left (+z), mode and control dials, shutter.
  g.add(box(30, 8, 34, mat.body, -4, H / 2 + 3, W / 2 - 22, 3));
  const dial = cylinder(9, 7, mat.body, "y", 32); dial.position.set(-6, H / 2 + 3.5, -W / 2 + 34); g.add(dial);
  const dial2 = cylinder(7, 6, mat.body, "y", 32); dial2.position.set(-12, H / 2 + 3, -W / 2 + 14); g.add(dial2);
  const shutter = cylinder(4.5, 4, mat.horn, "y", 24); shutter.position.set(16, H / 2 + 1, -W / 2 + 12); g.add(shutter);
  // Rear screen.
  g.add(box(2, 46, 74, mat.screen, -D / 2 - 1, -4, 8));
  // Lens mount and lens (front, +x), centred a little left of the grip.
  const lz = 10, ly = -2;
  const mount = cylinder(30, 6, mat.horn, "x"); mount.position.set(D / 2 + 3, ly, lz); g.add(mount);
  const barrel = cylinder(32, 52, mat.lens, "x"); barrel.position.set(D / 2 + 6 + 26, ly, lz); g.add(barrel);
  const ring = cylinder(33, 10, mat.rubber, "x"); ring.position.set(D / 2 + 6 + 30, ly, lz); g.add(ring);
  const front = cylinder(27, 2, mat.glass, "x"); front.position.set(D / 2 + 6 + 52 + 1, ly, lz); g.add(front);
  // Ring flash: a white diffuser ring round the lens front, on a dark housing.
  const fx = D / 2 + 6 + 52 + 6;
  const housing = new THREE.Mesh(new THREE.TorusGeometry(48, 9, 20, 64), mat.flashBody);
  housing.rotation.y = Math.PI / 2; housing.position.set(fx, ly, lz); housing.castShadow = true; g.add(housing);
  const diffuser = new THREE.Mesh(new THREE.RingGeometry(40, 56, 64), mat.flash.clone()); // own material: it flashes
  diffuser.rotation.y = Math.PI / 2; diffuser.position.set(fx + 9.2, ly, lz); g.add(diffuser);
  // The flash's light (off until a photo; see flashTick).
  const light = new THREE.PointLight(0xfff6e8, 0, 1400, 0);
  light.position.set(fx + 30, ly, lz); g.add(light);
  g.userData.flash = { mat: diffuser.material, light };
  // Its controller on the hot shoe.
  g.add(box(30, 26, 44, mat.flashBody, 2, H / 2 + 17, -10, 3));
  g.userData.lensFront = new THREE.Vector3(fx + 10, ly, lz);
  return g;
}

// ---------------------------------------------------------------- the object
// #3DBenchy (3DBenchy.com, CC0 1.0) on the turntable, scaled to the rig's
// ObjectZ height: Z-up in the file, so turned Y-up, centred, standing on 0,
// 1 mm tall (scaled per use).
let benchyGeo = null;
function benchy() {
  if (!benchyGeo) {
    benchyGeo = new STLLoader().loadAsync("./models/3dbenchy.stl").then((g) => {
      g.rotateX(-Math.PI / 2);
      g.computeBoundingBox();
      const h = g.boundingBox.max.y - g.boundingBox.min.y;
      g.scale(1 / h, 1 / h, 1 / h);
      g.computeBoundingBox();
      const b = g.boundingBox;
      g.translate(-(b.min.x + b.max.x) / 2, -b.min.y, -(b.min.z + b.max.z) / 2);
      g.computeVertexNormals();
      return g;
    });
  }
  return benchyGeo;
}

// ---------------------------------------------------------------- the camera's path
// makePath: the path through the shots in shooting order, as elevation and
// azimuth. at(i, f) is the point at fraction f of the hop from shot i to i+1.
// Azimuth is unwrapped (each hop the short way), so a spiral keeps turning.
// Stopping shots go straight from shot to shot; moving shots follow a smooth
// curve through them (Catmull-Rom), with no sharp corners.
function makePath(list, smooth) {
  const e = list.map((s) => s.elevation), az = [list[0]?.azimuth ?? 0];
  for (let i = 1; i < list.length; i++) az.push(az[i - 1] + (((list[i].azimuth - list[i - 1].azimuth + 540) % 360) - 180));
  const n = list.length;
  const cr = (p0, p1, p2, p3, t) => 0.5 * ((2 * p1) + (-p0 + p2) * t + (2 * p0 - 5 * p1 + 4 * p2 - p3) * t * t + (-p0 + 3 * p1 - 3 * p2 + p3) * t * t * t);
  function at(i, f) {
    if (!smooth) return { e: e[i] + (e[i + 1] - e[i]) * f, az: az[i] + (az[i + 1] - az[i]) * f };
    const a = Math.max(0, i - 1), b = i, c = i + 1, d = Math.min(n - 1, i + 2);
    return { e: cr(e[a], e[b], e[c], e[d], f), az: cr(az[a], az[b], az[c], az[d], f) };
  }
  return { at, smooth, n };
}

// ---------------------------------------------------------------- scene
export function createRig(container) {
  const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  renderer.shadowMap.enabled = true;
  renderer.shadowMap.type = THREE.PCFSoftShadowMap;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  container.appendChild(renderer.domElement);

  const scene = new THREE.Scene();
  const cam = new THREE.PerspectiveCamera(40, 1, 10, 20000);
  const controls = new OrbitControls(cam, renderer.domElement);
  controls.enableDamping = true;
  controls.minDistance = 300;
  controls.maxDistance = 6000;
  controls.maxPolarAngle = Math.PI * 0.55;

  // Lights: sky/ground fill, a key light with soft shadows, a rim light.
  scene.add(new THREE.HemisphereLight(0xdfe7ff, 0x30302c, 1.1));
  const key = new THREE.DirectionalLight(0xffffff, 2.2);
  key.position.set(-900, 1600, 1100);
  key.castShadow = true;
  key.shadow.mapSize.set(2048, 2048);
  Object.assign(key.shadow.camera, { left: -900, right: 900, top: 900, bottom: -900, near: 100, far: 4000 });
  key.shadow.bias = -0.0004;
  key.shadow.radius = 4;
  scene.add(key);
  const rim = new THREE.DirectionalLight(0xbfd2ff, 0.8);
  rim.position.set(1200, 900, -1400);
  scene.add(rim);

  // Floor: a shadow catcher with a faint grid.
  const floor = new THREE.Mesh(new THREE.PlaneGeometry(6000, 6000), new THREE.ShadowMaterial({ opacity: 0.28 }));
  floor.rotation.x = -Math.PI / 2;
  floor.receiveShadow = true;
  scene.add(floor);
  const grid = new THREE.GridHelper(3000, 30, 0x888888, 0x888888);
  grid.material.opacity = 0.12; grid.material.transparent = true;
  scene.add(grid);

  const shotColors = { pending: new THREE.Color(0x8a93a3), done: new THREE.Color(0x3ccf7a) };
  const V = (x, y, z) => new THREE.Vector3(x, y, z);
  let rig = null;       // the built rig: parts that move, and its measurements
  let pv = null;        // running path preview (see startPreview)
  let last = {};        // last update, re-applied after a rebuild
  let cv = null;        // running capture's comet (see captureTick)
  let samples = [];     // recent telemetry poses, for smooth motion (see livePose)

  // build makes the rig from measurements (three.js: x = X, y = Z up, z = Y).
  function build(d) {
    if (rig) scene.remove(rig.root);
    const root = new THREE.Group();
    scene.add(root);
    const postY = d.BaseY / 2 - P / 2;         // post centres
    const pivotY = pivotHeight(d);             // servo horn axis height
    const armZ = d.SwingY / 2 - P / 2;         // swing arm centres
    const barX = d.SwingX / 2 - P / 2;         // camera bar distance from the axis

    // Base frame: the X sides full length, the Y sides between them.
    const bx = d.BaseX / 2, by = d.BaseY / 2 - P / 2, y0 = P / 2;
    root.add(profile(V(-bx, y0, -by), V(bx, y0, -by)));
    root.add(profile(V(-bx, y0, by), V(bx, y0, by)));
    root.add(profile(V(-bx + P / 2, y0, -by + P / 2), V(-bx + P / 2, y0, by - P / 2)));
    root.add(profile(V(bx - P / 2, y0, -by + P / 2), V(bx - P / 2, y0, by - P / 2)));
    // Posts in the middle of the X sides, a servo on each, horns facing in.
    for (const s of [-1, 1]) {
      root.add(profile(V(0, P, s * postY), V(0, P + d.PostZ, s * postY)));
      const sv = st3215();
      sv.position.set(0, pivotY, s * postY);
      if (s < 0) sv.rotation.y = Math.PI; // horn towards the middle
      root.add(sv);
    }

    // Swing (tilting frame): turns about the axis through the servo horns.
    const tilt = new THREE.Group();
    tilt.position.set(0, pivotY, 0);
    root.add(tilt);
    const ax = d.SwingX / 2;
    for (const s of [-1, 1]) tilt.add(profile(V(-ax, 0, s * armZ), V(ax, 0, s * armZ)));
    for (const s of [-1, 1]) tilt.add(profile(V(s * barX, 0, -armZ + P / 2), V(s * barX, 0, armZ - P / 2)));
    // The camera sits on the middle of the -X bar, looking at the axis,
    // CameraOffset along the arms (+ towards the object).
    const camera = a6600();
    camera.position.set(-barX + d.CameraOffset, P / 2 + 34, 0);
    tilt.add(camera);
    const sightGeo = new THREE.BufferGeometry().setFromPoints([V(0, 0, 0), V(0, 0, 0)]);
    const sight = new THREE.Line(sightGeo, new THREE.LineDashedMaterial({ color: 0x5b8cff, dashSize: 14, gapSize: 10, transparent: true, opacity: 0.7 }));
    root.add(sight);

    // Turntable on a thin post (10 mm), with a 0° mark and the object: #3DBenchy.
    const pedH = Math.max(1, d.TurntableZ - P - 12);
    const ped = cylinder(5, pedH, mat.alu, "y", 24); ped.position.set(0, P + pedH / 2, 0); root.add(ped);
    const turn = new THREE.Group();
    turn.position.set(0, d.TurntableZ, 0);
    root.add(turn);
    const discR = Math.min(120, d.BaseY / 2 - 40);
    const disc = cylinder(discR, 12, mat.table, "y", 64); disc.position.y = -6; turn.add(disc);
    turn.add(box(18, 2, 6, new THREE.MeshStandardMaterial({ color: 0x5b8cff, emissive: 0x1a2a55 }), discR - 14, 1, 0));
    benchy().then((geo) => {
      const obj = new THREE.Mesh(geo, mat.object);
      obj.scale.setScalar(d.ObjectZ); // proportionally, to the object's height
      obj.castShadow = obj.receiveShadow = true;
      turn.add(obj);
    });

    // Shots: spheres where the camera will be, turning with the platform.
    const shotGroup = new THREE.Group();
    shotGroup.position.set(0, pivotY, 0);
    root.add(shotGroup);
    const nextRing = new THREE.Mesh(new THREE.TorusGeometry(11, 2, 12, 32), new THREE.MeshBasicMaterial({ color: 0x5b8cff }));
    nextRing.visible = false;
    shotGroup.add(nextRing);

    // Dot flashes (see flashTick): a few glowing spheres, reused.
    const pops = [];
    for (let k = 0; k < 6; k++) {
      const m = new THREE.Mesh(new THREE.SphereGeometry(1, 16, 12),
        new THREE.MeshBasicMaterial({ color: 0xfff6e8, transparent: true, opacity: 0, blending: THREE.AdditiveBlending, depthWrite: false }));
      m.visible = false;
      shotGroup.add(m);
      pops.push({ m, age: Infinity });
    }

    rig = { d, root, tilt, turn, camera, sight, sightGeo, shotGroup, nextRing, shotMesh: null, shotKey: "", pops, shotR: 5,
      orbit: barX - d.CameraOffset + 40, // camera (lens) distance from the tilt axis
      objectCentre: V(0, d.TurntableZ + d.ObjectZ / 2, 0), // the object's middle
      target: V(0, (pivotY + d.TurntableZ) / 2, 0) };
  }

  // shotPos: where the camera is, relative to the object, for a shot taken at
  // elevation e, azimuth a (in the turntable's frame, about the tilt axis).
  function shotPos(e, a) {
    const v = V(-Math.cos(rad(e)), Math.sin(rad(e)), 0).multiplyScalar(rig.orbit);
    return v.applyAxisAngle(V(0, 1, 0), -rad(a));
  }

  // ---------------------------------------------------------------- update
  function update(u) {
    last = u;
    const { shots, index, running } = u;
    addSample(u);
    // The swing and platform are posed every frame (see the animation loop).
    // Each capture has its own trail: a new one drops what's left of the last.
    const capturing = running && (index ?? 0) < (shots?.length ?? 0);
    if (cv && capturing && cv.ended) { dropComet(cv); cv = null; }
    if (capturing && !cv) startCapture();
    if (cv) captureShots(u);

    // Shots: rebuild when the plan changes, recolour as they're taken.
    const list = shots || [];
    const key = (u.smooth ? "smooth:" : "") + list.map((s) => `${s.elevation},${s.azimuth}`).join(";");
    if (key !== rig.shotKey) {
      rig.shotKey = key;
      if (rig.shotMesh) { rig.shotGroup.remove(rig.shotMesh); rig.shotMesh.geometry.dispose(); }
      rig.shotMesh = null;
      // The intended path: a very faint line through the shots in shooting
      // order, along the curves the camera will take.
      if (rig.pathLine) { rig.shotGroup.remove(rig.pathLine); rig.pathLine.geometry.dispose(); rig.pathLine = null; }
      if (list.length > 1) {
        const pts = [];
        const path = makePath(list, !!u.smooth);
        for (let i = 0; i + 1 < list.length; i++) for (let k = 0; k < 12; k++) { const q = path.at(i, k / 12); pts.push(shotPos(q.e, q.az)); }
        pts.push(shotPos(list[list.length - 1].elevation, list[list.length - 1].azimuth));
        rig.pathLine = new THREE.Line(new THREE.BufferGeometry().setFromPoints(pts),
          new THREE.LineBasicMaterial({ color: 0x8fb1ff, transparent: true, opacity: 0.16, depthWrite: false }));
        rig.shotGroup.add(rig.pathLine);
      }
      if (list.length) {
        const r = rig.shotR = Math.max(3, Math.min(7, 110 / Math.sqrt(list.length)));
        rig.shotMesh = new THREE.InstancedMesh(new THREE.SphereGeometry(r, 16, 12),
          new THREE.MeshStandardMaterial({ roughness: 0.4, transparent: true, opacity: 0.85 }), list.length);
        rig.shotGroup.add(rig.shotMesh);
      }
    }
    if (rig.shotMesh) {
      const m = new THREE.Matrix4();
      list.forEach((s, i) => {
        const p = shotPos(s.done ? s.actualElevation ?? s.elevation : s.elevation, s.done ? s.actualAzimuth ?? s.azimuth : s.azimuth);
        if ((pv && i < pv.consumed) || (cv && s.done)) m.makeScale(0, 0, 0); // consumed by the path preview or capture
        else m.makeTranslation(p.x, p.y, p.z);
        rig.shotMesh.setMatrixAt(i, m);
        rig.shotMesh.setColorAt(i, s.done ? shotColors.done : shotColors.pending);
      });
      rig.shotMesh.instanceMatrix.needsUpdate = true;
      if (rig.shotMesh.instanceColor) rig.shotMesh.instanceColor.needsUpdate = true;
    }
    const next = running && list[index];
    rig.nextRing.visible = !!next && !pv && !cv;
    if (next) {
      rig.nextRing.position.copy(shotPos(next.elevation, next.azimuth));
      rig.nextRing.lookAt(V(0, 0, 0)); // face the object
    }
  }

  // setDims rebuilds the rig when its measurements change.
  function setDims(d) {
    const dims = { ...DEFAULT_RIG, ...(d || {}) };
    if (rig && JSON.stringify(rig.d) === JSON.stringify(dims)) return;
    const first = !rig;
    stopPreview();
    build(dims);
    controls.target.copy(rig.target);
    if (first) setView("3d");
    update(last);
  }

  // ---------------------------------------------------------------- views
  // Front looks at the camera through the object (from +X); side looks along
  // the tilt axis (from +Y), the swing moving in the picture; top has the
  // camera on the left.
  function setView(name) {
    const d = rig.d, t = rig.target;
    const far = Math.max(d.BaseX, d.BaseY, d.PostZ + 300) * 2.6;
    const v = { "3d": [far * 0.7, t.y + far * 0.43, far * 0.54], front: [far, t.y, 0], side: [0, t.y, far], top: [0, far * 1.1, 1] }[name] || null;
    if (!v) return;
    cam.position.set(v[0], v[1], v[2]);
    controls.target.copy(t);
    controls.update();
  }

  function resize() {
    const w = container.clientWidth, h = container.clientHeight;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    renderer.domElement.style.width = w + "px";
    renderer.domElement.style.height = h + "px";
    cam.aspect = w / h;
    cam.updateProjectionMatrix();
  }
  new ResizeObserver(resize).observe(container);
  resize();
  setDims(DEFAULT_RIG);
  // ---------------------------------------------------------------- path preview
  // A ball shows the camera going through the shots in shooting order at
  // PREVIEW_SPEED, as the rig really does it: the camera only tilts
  // (elevation) while the turntable turns the object (azimuth). So the ball
  // stays on the camera's arc, and the turntable, the object, the sphere of
  // shots and the ball's fading tail (about 5 hops long) turn together,
  // while the swing tilts the camera to each elevation.
  const PREVIEW_SPEED = 90; // degrees per second, along the path
  const TRAIL_N = 160;
  // hopAngle: the angle (degrees) the camera turns through round the object
  // on hop i of a path.
  function hopAngle(path, i) {
    const pos = (f) => { const q = path.at(i, f); return shotPos(q.e, q.az); };
    let sum = 0, prev = pos(0);
    for (let k = 1; k <= 16; k++) {
      const p = pos(k / 16);
      sum += prev.angleTo(p);
      prev = p;
    }
    return sum * 180 / Math.PI;
  }
  // A comet: the ball (where the camera is) and its tail, in the turntable's
  // frame so they turn with the platform. The tail is a thin tube along the
  // camera's recent path, coloured by the camera's speed there against the
  // path's average (normal): neutral at normal, warming to amber and coral
  // when faster, cooling to blue when slower; it fades and thins towards its
  // end.
  const TUBE_R = 6; // sides of the tube
  function makeComet() {
    const ball = new THREE.Mesh(new THREE.SphereGeometry(9, 24, 16), new THREE.MeshBasicMaterial({ color: 0x8fb1ff }));
    const geo = new THREE.BufferGeometry();
    geo.setAttribute("position", new THREE.BufferAttribute(new Float32Array(TRAIL_N * TUBE_R * 3), 3).setUsage(THREE.DynamicDrawUsage));
    geo.setAttribute("color", new THREE.BufferAttribute(new Float32Array(TRAIL_N * TUBE_R * 4), 4).setUsage(THREE.DynamicDrawUsage)); // RGBA
    const idx = [];
    for (let k = 0; k + 1 < TRAIL_N; k++) for (let j = 0; j < TUBE_R; j++) {
      const a = k * TUBE_R + j, b = k * TUBE_R + (j + 1) % TUBE_R, c = a + TUBE_R, d = b + TUBE_R;
      idx.push(a, c, b, b, c, d);
    }
    geo.setIndex(idx);
    geo.setDrawRange(0, 0);
    const trail = new THREE.Mesh(geo, new THREE.MeshBasicMaterial({ vertexColors: true, transparent: true,
      depthWrite: false, side: THREE.DoubleSide }));
    trail.frustumCulled = false;
    ball.visible = false;
    rig.shotGroup.add(ball, trail);
    return { ball, trail, history: [], normal: 0 };
  }
  function dropComet(c) {
    rig.shotGroup.remove(c.ball, c.trail);
    c.ball.geometry.dispose(); c.trail.geometry.dispose();
  }
  // record adds where the ball is at time t to the tail, with the camera's
  // speed round the object (degrees per second, smoothed over ~0.15 s).
  function record(c, t, p) {
    const h = c.history, prev = h[h.length - 1];
    let v = prev ? prev.v : c.normal;
    if (prev && t > prev.t) v += (prev.p.angleTo(p) * 180 / Math.PI / (t - prev.t) - v) * Math.min(1, (t - prev.t) / 0.15);
    else if (prev) return;
    h.push({ t, p: p.clone(), v });
    return v;
  }
  // speedColor: a diverging scale of even brightness, on a log scale so
  // faster and slower look alike: blue at half the normal speed or less,
  // neutral at normal, amber at 1.4×, coral at twice or more. The neutral
  // suits the theme (light on dark, slate on light); the legend in the page
  // uses the same stops (SPEED_STOPS).
  const dark = matchMedia("(prefers-color-scheme: dark)");
  const SPEED_STOPS = { slow: 0x4c9be8, normal: [0xe6e9ef, 0x4a5262], fast: 0xf2a03d, fastest: 0xe8553c };
  const sc = { slow: new THREE.Color(SPEED_STOPS.slow), fast: new THREE.Color(SPEED_STOPS.fast), fastest: new THREE.Color(SPEED_STOPS.fastest),
    normal: [new THREE.Color(SPEED_STOPS.normal[0]), new THREE.Color(SPEED_STOPS.normal[1])] };
  const speedX = (v, normal) => normal > 0 ? Math.max(-1, Math.min(1, Math.log2(Math.max(v, 1e-3) / normal))) : 0;
  function speedColor(out, v, normal) {
    const x = speedX(v, normal), mid = sc.normal[dark.matches ? 0 : 1];
    if (x < 0) return out.copy(mid).lerp(sc.slow, -x);
    if (x < 0.5) return out.copy(mid).lerp(sc.fast, x / 0.5);
    return out.copy(sc.fast).lerp(sc.fastest, (x - 0.5) / 0.5);
  }
  // speed: the camera's speed now and the normal (degrees per second), for
  // the legend; null when there's no trail.
  function speed() {
    const c = pv || cv, h = c?.history;
    if (!h?.length || !(c.normal > 0)) return null;
    const v = h[h.length - 1].v;
    return { v, normal: c.normal, x: speedX(v, c.normal), moving: c === pv ? pv.t <= pv.times[pv.times.length - 1] : !(cv.ended && cv.clock >= cv.endAt) };
  }
  // drawTrail: the tail at time `now`, through the history younger than
  // `tail` seconds (evenly sampled, ending at the newest point), fading
  // (more transparent) and thinning towards its end.
  const tN = V(0, 0, 0), tB = V(0, 0, 0), tT = V(0, 0, 0), tQ = V(0, 0, 0);
  function drawTrail(c, now, tail) {
    while (c.history.length && c.history[0].t < now - tail) c.history.shift();
    const h = c.history, n = Math.min(TRAIL_N, h.length);
    const geo = c.trail.geometry, pos = geo.attributes.position.array, col = geo.attributes.color.array;
    const pts = [];
    for (let k = 0; k < n; k++) pts.push(h[n < 2 ? 0 : Math.round((k / (n - 1)) * (h.length - 1))]);
    for (let k = 0; k < n; k++) {
      const e = pts[k], age = Math.min(1, Math.max(0, (now - e.t) / tail)); // 0 = new, 1 = end of the tail
      tT.subVectors(pts[Math.min(n - 1, k + 1)].p, pts[Math.max(0, k - 1)].p);
      if (tT.lengthSq() < 1e-9) tT.set(0, 1, 0);
      tT.normalize();
      tN.copy(e.p).normalize().cross(tT); // across the path, on the sphere
      if (tN.lengthSq() < 1e-9) tN.set(1, 0, 0).cross(tT);
      tN.normalize();
      tB.crossVectors(tT, tN);
      const r = 3.2 * (1 - age * 0.8);
      speedColor(tmpC, e.v, c.normal);
      const alpha = Math.pow(1 - age, 1.6) * 0.9;
      for (let j = 0; j < TUBE_R; j++) {
        const a = (j / TUBE_R) * Math.PI * 2, o = (k * TUBE_R + j) * 3, q = (k * TUBE_R + j) * 4;
        tQ.copy(e.p).addScaledVector(tN, Math.cos(a) * r).addScaledVector(tB, Math.sin(a) * r);
        pos[o] = tQ.x; pos[o + 1] = tQ.y; pos[o + 2] = tQ.z;
        col[q] = tmpC.r; col[q + 1] = tmpC.g; col[q + 2] = tmpC.b; col[q + 3] = alpha;
      }
    }
    geo.setDrawRange(0, Math.max(0, n - 1) * TUBE_R * 6);
    geo.attributes.position.needsUpdate = geo.attributes.color.needsUpdate = true;
  }
  function startPreview(shots, smooth) {
    stopPreview();
    if (cv?.ended) { dropComet(cv); cv = null; update(last); } // a finished capture's fading trail
    if (!shots || shots.length < 2 || cv) return Promise.resolve();
    rig.nextRing.visible = false;
    // Each hop takes its path angle / PREVIEW_SPEED; times[i] is when the
    // ball leaves shot i.
    const times = [0];
    const path = makePath(shots, !!smooth);
    for (let i = 0; i + 1 < shots.length; i++) times.push(times[i] + Math.max(0.05, hopAngle(path, i) / PREVIEW_SPEED));
    const tail = 5 * times[times.length - 1] / (shots.length - 1);
    let done;
    const finished = new Promise((r) => (done = r));
    pv = { ...makeComet(), shots, path, times, tail, t: 0, clock: 0, done, consumed: 0 };
    let total = 0;
    for (let i = 0; i + 1 < shots.length; i++) total += hopAngle(path, i);
    pv.normal = total / times[times.length - 1]; // the average speed along the path
    return finished;
  }
  function stopPreview() {
    if (!pv) return;
    dropComet(pv);
    const done = pv.done;
    pv = null;
    update(last);
    done();
  }
  // previewState: the elevation and azimuth after t seconds. Stopping shots
  // ease in and out of each shot like the servos; moving shots keep going.
  function previewState(t) {
    const ts = pv.times;
    let i = 0;
    while (i < ts.length - 2 && t >= ts[i + 1]) i++;
    let f = Math.min(1, Math.max(0, (t - ts[i]) / (ts[i + 1] - ts[i])));
    if (!pv.path.smooth) f = f * f * (3 - 2 * f);
    return pv.path.at(i, f);
  }
  const tmpM = new THREE.Matrix4(), tmpC = new THREE.Color();
  // The preview starts with the swing and platform taking LEAD seconds to go
  // from where the rig physically is to the first shot. After the last shot
  // the camera holds there for HOLD seconds (none), then the swing and platform take
  // BACK seconds to return to the rig's pose. The trail keeps catching up with
  // the last shot at its own pace meanwhile, even once the camera has gone;
  // the preview ends when the camera is back and the trail has run out.
  const LEAD = 1, HOLD = 0, BACK = 1;
  // glide eases from pose `from` to `to` (elevation, azimuth the short way).
  function glide(from, to, f) {
    f = Math.min(1, Math.max(0, f));
    f = f * f * (3 - 2 * f);
    const daz = ((to.az - from.az) % 360 + 540) % 360 - 180;
    setPose(from.e + (to.e - from.e) * f, from.az + daz * f);
  }

  // ---------------------------------------------------------------- live pose
  // Telemetry comes ~10 times a second; the view plays it back DELAY behind,
  // interpolating between frames, so the rig moves smoothly as it really does.
  const DELAY = 150; // ms
  function addSample(u) {
    if (u.elevation === null || u.elevation === undefined) { samples = []; return; }
    const tm = u.time ?? performance.now();
    if (samples.length && tm <= samples[samples.length - 1].tm) return; // not a new frame
    samples.push({ tm, at: performance.now(), e: u.elevation, az: u.azimuth ?? 0 });
    if (samples.length > 20) samples.shift();
  }
  function livePose() {
    const n = samples.length;
    if (!n) return { e: last.elevation ?? 0, az: last.azimuth ?? 0 };
    const b = samples[n - 1];
    const tm = b.tm + (performance.now() - b.at) - DELAY; // telemetry time now shown
    if (tm >= b.tm || n < 2) return { e: b.e, az: b.az };
    let i = n - 1;
    while (i > 0 && samples[i - 1].tm > tm) i--;
    if (i === 0) return { e: samples[0].e, az: samples[0].az };
    const a = samples[i - 1], c = samples[i], f = (tm - a.tm) / (c.tm - a.tm);
    const daz = ((c.az - a.az) % 360 + 540) % 360 - 180;
    return { e: a.e + (c.e - a.e) * f, az: a.az + daz * f };
  }
  // setPose puts the swing and platform at elevation e, azimuth az.
  function setPose(e, az) {
    rig.turn.rotation.y = rig.shotGroup.rotation.y = rad(az);
    rig.tilt.rotation.z = -rad(e);
    rig.tilt.updateMatrixWorld(true);
    rig.sightGeo.setFromPoints([rig.camera.localToWorld(rig.camera.userData.lensFront.clone()), rig.objectCentre]);
    rig.sight.computeLineDistances();
  }
  function previewTick(dt) {
    if (!pv) return;
    pv.clock += dt;
    if (pv.clock < LEAD) { // from the rig's pose to the first shot
      pv.ball.visible = false;
      glide(livePose(), previewState(0), pv.clock / LEAD);
      return;
    }
    pv.ball.visible = true;
    pv.t = pv.clock - LEAD; // time along the path
    const end = pv.times[pv.times.length - 1];
    const t = Math.min(pv.t, end);
    if (pv.t > end + HOLD) {
      // The camera returns to the rig's real pose (and stays with it); the
      // ball goes, the trail carries on below.
      pv.ball.visible = false;
      glide(previewState(end), livePose(), (pv.t - end - HOLD) / BACK);
    } else {
      // The turntable side (object, sphere, tail) turns to the azimuth; the
      // ball, placed in that turning frame, ends up on the camera's fixed arc.
      const st = previewState(t);
      setPose(st.e, st.az);
      pv.ball.position.copy(shotPos(st.e, st.az));
    }
    // Dots the ball has reached are consumed: hidden until the preview ends
    // (update() puts them back).
    const mesh = rig.shotMesh;
    if (mesh && mesh.count === pv.shots.length) {
      let changed = false;
      while (pv.consumed < pv.times.length && t >= pv.times[pv.consumed]) {
        mesh.setMatrixAt(pv.consumed++, tmpM.makeScale(0, 0, 0));
        changed = true;
      }
      if (changed) {
        mesh.instanceMatrix.needsUpdate = true;
        const s = pv.shots[pv.consumed - 1];
        flash(shotPos(s.elevation, s.azimuth)); // the photo
      }
    }
    // The trail: recorded while moving; afterwards it keeps ageing, so it
    // catches up with the last shot at the same pace.
    if (pv.t <= end) record(pv, t, pv.ball.position);
    drawTrail(pv, pv.t, pv.tail);
    if (pv.t >= end + HOLD + BACK && !pv.history.length) stopPreview();
  }

  // ---------------------------------------------------------------- capture
  // During a capture the view looks like the preview, but follows the real
  // rig: the ball is where the camera really is, its tail is where it really
  // went, and each shot's dot goes once the photo is taken. The tail is about
  // 5 shots long (in time, from the recent pace), and only drawn between
  // the first shot and the last: not on the way to the first or back home.
  // When the last photo is taken (or the capture stops) the ball goes and the
  // tail runs out; then the dots come back, taken ones green.
  function startCapture() {
    stopPreview();
    cv = { ...makeComet(), clock: 0, index: last.index ?? 0, shotAt: [], tail: 2, dist: 0, moving: 0, flashes: [] };
  }
  function captureShots(u) {
    // Ended: the last photo taken, or stopped. The view runs DELAY behind.
    if (!cv.ended && (!u.running || (u.index ?? 0) >= (u.shots?.length ?? 0))) { cv.ended = true; cv.endAt = cv.clock + DELAY / 1000; }
    if ((u.index ?? 0) > cv.index) {
      const s = u.shots?.[u.index - 1];
      if (s) cv.flashes.push({ at: cv.clock + DELAY / 1000, p: shotPos(s.actualElevation ?? s.elevation, s.actualAzimuth ?? s.azimuth) });
      cv.index = u.index;
      cv.shotAt.push(cv.clock);
      const at = cv.shotAt.slice(-6);
      if (at.length > 1) cv.tail = Math.max(0.5, 5 * (at[at.length - 1] - at[0]) / (at.length - 1));
    }
  }
  function captureTick(dt) {
    if (!cv) return;
    cv.clock += dt;
    // Photos taken show as the view catches up (it runs DELAY behind).
    while (cv.flashes.length && cv.clock >= cv.flashes[0].at) flash(cv.flashes.shift().p);
    const running = !(cv.ended && cv.clock >= cv.endAt); // moving through this capture's shots
    cv.ball.visible = running;
    if (running) {
      const q = livePose();
      cv.ball.position.copy(shotPos(q.e, q.az));
    }
    // The tail starts at the first shot (as shown: the view runs DELAY behind).
    if (running && cv.shotAt.length && cv.clock >= cv.shotAt[0] + DELAY / 1000) {
      // Normal: the average speed so far (path length over time).
      const prev = cv.history[cv.history.length - 1];
      if (prev && cv.clock > prev.t) { cv.dist += prev.p.angleTo(cv.ball.position) * 180 / Math.PI; cv.moving += cv.clock - prev.t; }
      if (cv.moving > 0.5) cv.normal = cv.dist / cv.moving;
      record(cv, cv.clock, cv.ball.position);
    }
    drawTrail(cv, cv.clock, cv.tail);
    if (!running && !cv.history.length) {
      dropComet(cv);
      cv = null;
      update(last); // the dots come back
    }
  }

  // ---------------------------------------------------------------- photo flash
  // When a photo is taken its dot pops (a glow that swells and fades) and
  // the ring flash fires lightly: the diffuser glows and briefly lights the
  // scene from the camera.
  const POP = 0.45, FLASH = 0.3; // s
  let flashAge = Infinity;
  const flashBase = new THREE.Color(0x2a2d33), flashHot = new THREE.Color(0xfff6e8);
  function flash(p) {
    const pop = rig.pops.reduce((a, b) => (b.age > a.age ? b : a)); // the oldest
    pop.age = 0;
    pop.m.position.copy(p);
    pop.m.visible = true;
    flashAge = 0;
  }
  function flashTick(dt) {
    for (const pop of rig.pops) {
      if (pop.age >= POP) { pop.m.visible = false; continue; }
      pop.age += dt;
      const f = Math.min(1, pop.age / POP);
      pop.m.scale.setScalar(rig.shotR * (1 + 2 * Math.sqrt(f)));
      pop.m.material.opacity = 0.85 * (1 - f) * (1 - f);
    }
    const fl = rig.camera.userData.flash;
    if (flashAge > FLASH) { fl.light.intensity = 0; fl.mat.emissive.copy(flashBase); return; }
    flashAge += dt;
    // A quick rise, then an exponential-ish fall.
    const k = flashAge < 0.03 ? flashAge / 0.03 : Math.pow(1 - Math.min(1, (flashAge - 0.03) / (FLASH - 0.03)), 2);
    fl.light.intensity = 1.2 * k;
    fl.mat.emissive.copy(flashBase).lerp(flashHot, k);
  }

  const clock = new THREE.Clock();
  renderer.setAnimationLoop(() => {
    const dt = Math.min(clock.getDelta(), 0.1);
    if (!pv) { const q = livePose(); setPose(q.e, q.az); } // during a preview the preview poses the rig
    previewTick(dt);
    captureTick(dt);
    flashTick(dt);
    controls.update();
    renderer.render(scene, cam);
  });
  // getView reports where the viewer is, relative to the rig's centre, as
  // fractions of the preset distance (used to pick the presets).
  function getView() {
    const d = rig.d, t = rig.target, far = Math.max(d.BaseX, d.BaseY, d.PostZ + 300) * 2.6;
    const v = cam.position.clone().sub(t).divideScalar(far);
    return { x: +v.x.toFixed(2), y: +v.y.toFixed(2), z: +v.z.toFixed(2) };
  }
  return { update, setView, setDims, getView, startPreview, stopPreview, previewing: () => !!pv, speed };
}

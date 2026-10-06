// Live 3D model of the photogrammetry rig, rendered with three.js.
//
// Units are millimetres. three.js is Y-up: x runs along the base's 600 mm
// sides, y up, z across the base (500 mm). The tilt axis runs along z
// through the two servos on top of the posts; the turntable turns about y.
import * as THREE from "three";
import { OrbitControls } from "./vendor/OrbitControls.js";
import { STLLoader } from "./vendor/STLLoader.js";

const P = 20; // 2020 profile size

// Default measurements (mm), as in gosts' config: seen from above, X along
// the base sides that carry the posts, Y along the tilt axis, Z up.
export const DEFAULT_RIG = { BaseX: 600, BaseY: 500, PostZ: 400, SwingX: 600, SwingY: 450, CameraOffset: -50, TurntableZ: 300, ObjectZ: 100 };

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
  const diffuser = new THREE.Mesh(new THREE.RingGeometry(40, 56, 64), mat.flash);
  diffuser.rotation.y = Math.PI / 2; diffuser.position.set(fx + 9.2, ly, lz); g.add(diffuser);
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
  let last = {};        // last update, re-applied after a rebuild

  // build makes the rig from measurements (three.js: x = X, y = Z up, z = Y).
  function build(d) {
    if (rig) scene.remove(rig.root);
    const root = new THREE.Group();
    scene.add(root);
    const postY = d.BaseY / 2 - P / 2;         // post centres
    const pivotY = P + d.PostZ + 30;           // servo horn axis height
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

    rig = { d, root, tilt, turn, camera, sight, sightGeo, shotGroup, nextRing, shotMesh: null, shotKey: "",
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
    const { elevation, azimuth, shots, index, running } = u;
    const e = elevation ?? 0, a = azimuth ?? 0;
    rig.tilt.rotation.z = -rad(e);
    rig.turn.rotation.y = rad(a);
    rig.shotGroup.rotation.y = rad(a);
    rig.tilt.updateMatrixWorld(true);
    rig.sightGeo.setFromPoints([rig.camera.localToWorld(rig.camera.userData.lensFront.clone()), rig.objectCentre]);
    rig.sight.computeLineDistances();

    // Shots: rebuild when the plan changes, recolour as they're taken.
    const list = shots || [];
    const key = list.map((s) => `${s.elevation},${s.azimuth}`).join(";");
    if (key !== rig.shotKey) {
      rig.shotKey = key;
      if (rig.shotMesh) { rig.shotGroup.remove(rig.shotMesh); rig.shotMesh.geometry.dispose(); }
      rig.shotMesh = null;
      if (list.length) {
        const r = Math.max(3, Math.min(7, 110 / Math.sqrt(list.length)));
        rig.shotMesh = new THREE.InstancedMesh(new THREE.SphereGeometry(r, 16, 12),
          new THREE.MeshStandardMaterial({ roughness: 0.4, transparent: true, opacity: 0.85 }), list.length);
        rig.shotGroup.add(rig.shotMesh);
      }
    }
    if (rig.shotMesh) {
      const m = new THREE.Matrix4();
      list.forEach((s, i) => {
        const p = shotPos(s.done ? s.actualElevation ?? s.elevation : s.elevation, s.done ? s.actualAzimuth ?? s.azimuth : s.azimuth);
        m.makeTranslation(p.x, p.y, p.z);
        rig.shotMesh.setMatrixAt(i, m);
        rig.shotMesh.setColorAt(i, s.done ? shotColors.done : shotColors.pending);
      });
      rig.shotMesh.instanceMatrix.needsUpdate = true;
      if (rig.shotMesh.instanceColor) rig.shotMesh.instanceColor.needsUpdate = true;
    }
    const next = running && list[index];
    rig.nextRing.visible = !!next;
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
  renderer.setAnimationLoop(() => { controls.update(); renderer.render(scene, cam); });
  // getView reports where the viewer is, relative to the rig's centre, as
  // fractions of the preset distance (used to pick the presets).
  function getView() {
    const d = rig.d, t = rig.target, far = Math.max(d.BaseX, d.BaseY, d.PostZ + 300) * 2.6;
    const v = cam.position.clone().sub(t).divideScalar(far);
    return { x: +v.x.toFixed(2), y: +v.y.toFixed(2), z: +v.z.toFixed(2) };
  }
  return { update, setView, setDims, getView };
}

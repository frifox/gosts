// Live 3D model of the photogrammetry rig, rendered with three.js.
//
// Units are millimetres. three.js is Y-up: x runs along the base's 600 mm
// sides, y up, z across the base (500 mm). The tilt axis runs along z
// through the two servos on top of the posts; the turntable turns about y.
import * as THREE from "three";
import { OrbitControls } from "./vendor/OrbitControls.js";

// Rig dimensions (mm).
const RIG = {
  baseX: 600, baseZ: 500,   // base frame, outer
  post: 600,                // vertical posts, in the middle of the 600 mm sides
  armLen: 600, barLen: 450, // tilting frame: 600 mm arms joined by 450 mm bars
  platformY: 330,           // turntable top (estimate)
  platformR: 120,
};
const P = 20;                       // 2020 profile size
const POST_Z = RIG.baseZ / 2 - P / 2;  // post centres
const PIVOT_Y = P + RIG.post + 30;  // servo horn axis height
const ARM_Z = RIG.barLen / 2 - P / 2;  // arm centres in the tilting frame
const ORBIT = RIG.armLen / 2 - P / 2;  // camera bar distance from the axis

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
  const target = new THREE.Vector3(0, 360, 0);
  const controls = new OrbitControls(cam, renderer.domElement);
  controls.target.copy(target);
  controls.enableDamping = true;
  controls.minDistance = 500;
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

  // Base frame: 600 mm long sides, 460 mm short sides between them.
  const bx = RIG.baseX / 2, bz = RIG.baseZ / 2 - P / 2, y0 = P / 2;
  const V = (x, y, z) => new THREE.Vector3(x, y, z);
  scene.add(profile(V(-bx, y0, -bz), V(bx, y0, -bz)));
  scene.add(profile(V(-bx, y0, bz), V(bx, y0, bz)));
  scene.add(profile(V(-bx + P / 2, y0, -bz + P / 2), V(-bx + P / 2, y0, bz - P / 2)));
  scene.add(profile(V(bx - P / 2, y0, -bz + P / 2), V(bx - P / 2, y0, bz - P / 2)));
  // Posts in the middle of the long sides, a servo on each, horns facing in.
  for (const s of [-1, 1]) {
    scene.add(profile(V(0, P, s * POST_Z), V(0, P + RIG.post, s * POST_Z)));
    const sv = st3215();
    sv.position.set(0, PIVOT_Y, s * POST_Z);
    if (s < 0) sv.rotation.y = Math.PI; // horn towards the middle
    scene.add(sv);
  }

  // Tilting frame: rotates about z through the servo horns.
  const tilt = new THREE.Group();
  tilt.position.set(0, PIVOT_Y, 0);
  scene.add(tilt);
  const ax = RIG.armLen / 2;
  for (const s of [-1, 1]) tilt.add(profile(V(-ax, 0, s * ARM_Z), V(ax, 0, s * ARM_Z)));
  for (const s of [-1, 1]) tilt.add(profile(V(s * (ax - P / 2), 0, -ARM_Z + P / 2), V(s * (ax - P / 2), 0, ARM_Z - P / 2)));
  // The camera sits on the middle of the -x bar, looking at the axis.
  const camera = a6600();
  camera.position.set(-ORBIT + 6, P / 2 + 34, 0);
  tilt.add(camera);
  // Line of sight to the middle of the object.
  const sightGeo = new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(), new THREE.Vector3()]);
  const sight = new THREE.Line(sightGeo, new THREE.LineDashedMaterial({ color: 0x5b8cff, dashSize: 14, gapSize: 10, transparent: true, opacity: 0.7 }));
  scene.add(sight);

  // Turntable on a pedestal, with a 0° mark and a placeholder object.
  const ped = cylinder(28, RIG.platformY - P - 10, mat.alu); ped.position.set(0, P + (RIG.platformY - P - 10) / 2, 0); scene.add(ped);
  const turn = new THREE.Group();
  turn.position.set(0, RIG.platformY, 0);
  scene.add(turn);
  const disc = cylinder(RIG.platformR, 12, mat.table, "y", 64); disc.position.y = -6; turn.add(disc);
  const mark = box(18, 2, 6, new THREE.MeshStandardMaterial({ color: 0x5b8cff, emissive: 0x1a2a55 }), RIG.platformR - 14, 1, 0);
  turn.add(mark);
  const objGeo = new THREE.LatheGeometry([[0, 0], [52, 0], [58, 18], [46, 70], [30, 120], [40, 170], [44, 200], [36, 230], [0, 236]].map(([x, y]) => new THREE.Vector2(x, y)), 64);
  const obj = new THREE.Mesh(objGeo, mat.object);
  obj.castShadow = obj.receiveShadow = true;
  turn.add(obj);
  const objectCentre = new THREE.Vector3(0, RIG.platformY + 118, 0);

  // Shots: spheres on the camera's orbit, turning with the platform.
  const shotGroup = new THREE.Group();
  shotGroup.position.set(0, PIVOT_Y, 0);
  scene.add(shotGroup);
  let shotMesh = null;
  const nextRing = new THREE.Mesh(new THREE.TorusGeometry(11, 2, 12, 32), new THREE.MeshBasicMaterial({ color: 0x5b8cff }));
  nextRing.visible = false;
  shotGroup.add(nextRing);
  const shotColors = { pending: new THREE.Color(0x8a93a3), done: new THREE.Color(0x3ccf7a) };

  // ---------------------------------------------------------------- update
  let shotKey = "";
  function update({ elevation, azimuth, shots, index, running }) {
    const e = elevation ?? 0, a = azimuth ?? 0;
    tilt.rotation.z = -rad(e);
    turn.rotation.y = rad(a);
    shotGroup.rotation.y = rad(a);
    tilt.updateMatrixWorld(true);
    const lens = camera.localToWorld(camera.userData.lensFront.clone());
    sightGeo.setFromPoints([lens, objectCentre]);
    sight.computeLineDistances();

    // Shots: rebuild when the plan changes, recolour as they're taken.
    const list = shots || [];
    const key = list.map((s) => `${s.elevation},${s.azimuth}`).join(";");
    if (key !== shotKey) {
      shotKey = key;
      if (shotMesh) { shotGroup.remove(shotMesh); shotMesh.geometry.dispose(); }
      shotMesh = null;
      if (list.length) {
        const r = Math.max(3, Math.min(7, 110 / Math.sqrt(list.length)));
        shotMesh = new THREE.InstancedMesh(new THREE.SphereGeometry(r, 16, 12),
          new THREE.MeshStandardMaterial({ roughness: 0.4, transparent: true, opacity: 0.85 }), list.length);
        shotGroup.add(shotMesh);
      }
    }
    if (shotMesh) {
      const m = new THREE.Matrix4();
      list.forEach((s, i) => {
        const p = shotPos(s.done ? s.actualElevation ?? s.elevation : s.elevation, s.done ? s.actualAzimuth ?? s.azimuth : s.azimuth);
        m.makeTranslation(p.x, p.y, p.z);
        shotMesh.setMatrixAt(i, m);
        shotMesh.setColorAt(i, s.done ? shotColors.done : shotColors.pending);
      });
      shotMesh.instanceMatrix.needsUpdate = true;
      if (shotMesh.instanceColor) shotMesh.instanceColor.needsUpdate = true;
    }
    const next = running && list[index];
    nextRing.visible = !!next;
    if (next) {
      const p = shotPos(next.elevation, next.azimuth);
      nextRing.position.copy(p);
      nextRing.lookAt(new THREE.Vector3()); // face the object
    }
  }
  // shotPos: where the camera is, relative to the object, for a shot taken at
  // elevation e, azimuth a (in the turntable's frame, about the tilt axis).
  function shotPos(e, a) {
    const v = new THREE.Vector3(-Math.cos(rad(e)), Math.sin(rad(e)), 0).multiplyScalar(ORBIT + 70);
    return v.applyAxisAngle(new THREE.Vector3(0, 1, 0), -rad(a));
  }

  // ---------------------------------------------------------------- views
  const VIEWS = { "3d": [1050, 760, 1150], front: [0, 420, 1750], side: [1750, 420, 0], top: [0, 1900, 1],
    camera: [-260, 820, 420] }; // close-up of the camera
  function setView(name) {
    const v = VIEWS[name] || VIEWS["3d"];
    cam.position.set(v[0], v[1], v[2]);
    controls.target.copy(name === "camera" ? camera.getWorldPosition(new THREE.Vector3()) : target);
    controls.update();
  }
  setView("3d");

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
  renderer.setAnimationLoop(() => { controls.update(); renderer.render(scene, cam); });
  return { update, setView };
}

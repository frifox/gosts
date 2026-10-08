"""Parametric turntable platform for the Waveshare ST3215 servo (Autodesk Fusion script).

Run: Utilities > Add-Ins > Scripts and Add-Ins > "+" > Script or add-in from device >
select this ServoPlatform folder > Run.

Builds a NEW parametric design. Everything is driven by User Parameters
(Modify > Change Parameters): edit a value and the model/timeline recomputes.
Z is the servo axis; Z=0 is the horn's top face, the platform grows towards +Z.

Servo data (from ST3215.step): horn disc dia 19.2, 4 bolt holes on a 14 mm circle
(0/90/180/270 deg), horn top face 2.1 mm above the highest point of the servo body,
so a flat platform base at Z=0 clears the body when it turns.
"""
import traceback
import adsk.core
import adsk.fusion

# (name, expression, units, comment)
PARAMS = [
    ('platform_d',      '76.2 mm', 'mm', 'Top disc diameter (3 in)'),
    ('platform_h',      '50.8 mm', 'mm', 'Top surface height above the servo horn face (2 in)'),
    ('top_t',           '4 mm',    'mm', 'Top disc thickness'),
    ('base_d',          '26 mm',   'mm', 'Base plate diameter (bolts to horn)'),
    ('base_t',          '4 mm',    'mm', 'Base plate thickness'),
    ('bolt_circle_d',   '14 mm',   'mm', 'Horn bolt circle diameter'),
    ('bolt_count',      '4',       '',   'Number of horn bolts'),
    ('bolt_hole_d',     '3.4 mm',  'mm', 'Bolt clearance hole (M3)'),
    ('center_clear_d',  '8 mm',    'mm', 'Clearance hole for the horn centre bolt'),
    ('access_d',        '9 mm',    'mm', 'Screwdriver access holes through the top disc'),
    ('cone_top_d',      '60 mm',   'mm', 'Support cone outer diameter at the top disc'),
    ('wall_t',          '2.4 mm',  'mm', 'Support cone wall thickness'),
    ('slot_len',        '6 mm',    'mm', 'Zip-tie slot length (radial)'),
    ('slot_w',          '2.4 mm',  'mm', 'Zip-tie slot width'),
    ('slot_r1',         '17 mm',   'mm', 'Slot ring 1 radius'),
    ('slot_n1',         '8',       '',   'Slot ring 1 count'),
    ('slot_r2',         '24 mm',   'mm', 'Slot ring 2 radius'),
    ('slot_n2',         '12',      '',   'Slot ring 2 count'),
    ('slot_r3',         '33 mm',   'mm', 'Slot ring 3 radius'),
    ('slot_n3',         '16',      '',   'Slot ring 3 count'),
]

app = adsk.core.Application.get()
ui = app.userInterface
P3 = adsk.core.Point3D.create
VI = adsk.core.ValueInput


def run(context):
    try:
        doc = app.documents.add(adsk.core.DocumentTypes.FusionDesignDocumentType)
        design = adsk.fusion.Design.cast(app.activeProduct)
        design.designType = adsk.fusion.DesignTypes.ParametricDesignType
        comp = design.rootComponent
        comp.name = 'ST3215 Platform'
        cm = lambda name: design.userParameters.itemByName(name).value  # internal cm

        for name, expr, units, comment in PARAMS:
            design.userParameters.add(name, VI.createByString(expr), units, comment)

        def plane_at(expr):
            pi = comp.constructionPlanes.createInput()
            pi.setByOffset(comp.xYConstructionPlane, VI.createByString(expr))
            return comp.constructionPlanes.add(pi)

        def dim(fn, *args, expr):
            """Add a sketch dimension and bind it to a parameter expression."""
            d = fn(*args)
            d.parameter.expression = expr
            return d

        def disc(sk, d_expr, x_expr=None):
            """Circle, diameter bound to d_expr; centred at the origin or at (x_expr, 0)."""
            x = cm_expr(x_expr) if x_expr else 0
            c = sk.sketchCurves.sketchCircles.addByCenterRadius(P3(x, 0, 0), cm_expr(d_expr) / 2)
            if x_expr:
                sk.geometricConstraints.addCoincident(c.centerSketchPoint, sk.xConstructionAxis)
                dim(sk.sketchDimensions.addDistanceDimension, sk.originPoint, c.centerSketchPoint,
                    adsk.fusion.DimensionOrientations.HorizontalDimensionOrientation,
                    P3(x / 2, -x, 0), expr=x_expr)
            else:
                sk.geometricConstraints.addCoincident(c.centerSketchPoint, sk.originPoint)
            dim(sk.sketchDimensions.addDiameterDimension, c, P3(0.7 * cm_expr(d_expr), 0.7 * cm_expr(d_expr), 0),
                expr=d_expr)
            return c

        def cm_expr(expr):
            return design.unitsManager.evaluateExpression(expr, 'cm')

        def all_profiles(sk):
            col = adsk.core.ObjectCollection.create()
            for p in sk.profiles:
                col.add(p)
            return col

        def extrude(profiles, op, dist_expr=None):
            ei = comp.features.extrudeFeatures.createInput(profiles, op)
            if dist_expr:
                ei.setDistanceExtent(False, VI.createByString(dist_expr))
            else:
                ei.setAllExtent(adsk.fusion.ExtentDirections.PositiveExtentDirection)
            return comp.features.extrudeFeatures.add(ei)

        def pattern(feature, count_name):
            col = adsk.core.ObjectCollection.create()
            col.add(feature)
            pi = comp.features.circularPatternFeatures.createInput(col, comp.zConstructionAxis)
            pi.quantity = VI.createByString(count_name)
            pi.totalAngle = VI.createByString('360 deg')
            pi.isSymmetric = False
            return comp.features.circularPatternFeatures.add(pi)

        JOIN = adsk.fusion.FeatureOperations.JoinFeatureOperation
        CUT = adsk.fusion.FeatureOperations.CutFeatureOperation
        NEW = adsk.fusion.FeatureOperations.NewBodyFeatureOperation

        base_top = plane_at('base_t')
        top_bot = plane_at('platform_h - top_t')
        top_bot.name = 'Top disc underside'

        # 1. base plate (sits on the horn face, Z = 0)
        sk = comp.sketches.add(comp.xYConstructionPlane); sk.name = 'Base plate'
        disc(sk, 'base_d')
        extrude(sk.profiles.item(0), NEW, 'base_t')

        # 2. top disc
        sk = comp.sketches.add(top_bot); sk.name = 'Top disc'
        disc(sk, 'platform_d')
        extrude(sk.profiles.item(0), JOIN, 'top_t')

        # 3. hollow support cone: solid loft, then hollow it with an inner loft cut
        sk_a = comp.sketches.add(base_top); sk_a.name = 'Cone bottom'
        disc(sk_a, 'base_d')
        sk_b = comp.sketches.add(top_bot); sk_b.name = 'Cone top'
        disc(sk_b, 'cone_top_d')
        li = comp.features.loftFeatures.createInput(JOIN)
        li.loftSections.add(sk_a.profiles.item(0))
        li.loftSections.add(sk_b.profiles.item(0))
        comp.features.loftFeatures.add(li)

        sk_c = comp.sketches.add(base_top); sk_c.name = 'Cavity bottom'
        disc(sk_c, 'base_d - 2 * wall_t')
        sk_d = comp.sketches.add(top_bot); sk_d.name = 'Cavity top'
        disc(sk_d, 'cone_top_d - 2 * wall_t')
        li = comp.features.loftFeatures.createInput(CUT)
        li.loftSections.add(sk_c.profiles.item(0))
        li.loftSections.add(sk_d.profiles.item(0))
        comp.features.loftFeatures.add(li)

        # 4. base plate: centre-bolt clearance + horn bolt holes (patterned)
        sk = comp.sketches.add(comp.xYConstructionPlane); sk.name = 'Centre bolt clearance'
        disc(sk, 'center_clear_d')
        extrude(sk.profiles.item(0), CUT, 'base_t')

        sk = comp.sketches.add(comp.xYConstructionPlane); sk.name = 'Horn bolt hole'
        disc(sk, 'bolt_hole_d', 'bolt_circle_d / 2')
        pattern(extrude(sk.profiles.item(0), CUT, 'base_t'), 'bolt_count')

        # 5. screwdriver access holes through the top disc, above each bolt
        sk = comp.sketches.add(top_bot); sk.name = 'Screwdriver access'
        disc(sk, 'access_d', 'bolt_circle_d / 2')
        pattern(extrude(sk.profiles.item(0), CUT), 'bolt_count')

        # 6. zip-tie slot rings through the top disc
        horiz = adsk.fusion.DimensionOrientations.HorizontalDimensionOrientation
        vert = adsk.fusion.DimensionOrientations.VerticalDimensionOrientation
        for i in (1, 2, 3):
            r, n = 'slot_r%d' % i, 'slot_n%d' % i
            rr, L, W = cm(r), cm('slot_len'), cm('slot_w')
            sk = comp.sketches.add(top_bot); sk.name = 'Zip-tie slot ring %d' % i
            ln = sk.sketchCurves.sketchLines.addTwoPointRectangle(
                P3(rr - L / 2, -W / 2, 0), P3(rr + L / 2, W / 2, 0))
            bottom, right, top = ln.item(0), ln.item(1), ln.item(2)
            top_left = top.endSketchPoint
            sk.geometricConstraints.addSymmetry(bottom.startSketchPoint, top_left, sk.xConstructionAxis)
            dim(sk.sketchDimensions.addDistanceDimension, bottom.startSketchPoint, bottom.endSketchPoint,
                horiz, P3(rr, -W, 0), expr='slot_len')
            dim(sk.sketchDimensions.addDistanceDimension, right.startSketchPoint, right.endSketchPoint,
                vert, P3(rr + L, 0, 0), expr='slot_w')
            dim(sk.sketchDimensions.addDistanceDimension, sk.originPoint, bottom.startSketchPoint,
                horiz, P3(rr / 2, -W * 2, 0), expr='%s - slot_len / 2' % r)
            pattern(extrude(sk.profiles.item(0), CUT), n)

        app.activeViewport.fit()
        ui.messageBox('ST3215 platform built.\nEdit dimensions in Modify > Change Parameters (User Parameters).')
    except Exception:
        ui.messageBox('Failed:\n' + traceback.format_exc())

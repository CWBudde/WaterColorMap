# WaterColorMap - Complete Rendering Elements by Zoom Level

This document provides a comprehensive breakdown of all rendering elements across zoom levels in WaterColorMap.

## Rendering Overview

### Layer Stack (Back to Front)

1. **Paper** - White textured base layer
2. **Land** - Tan/beige background (`#C4A574`)
3. **Urban** - Urban landuse areas (residential/commercial/industrial/retail) in lilac (`#C080C0`)
4. **Civic** - Civic areas (schools, hospitals, universities, libraries, town halls, stadiums) in lilac (`#C080C0`)
5. **Parks** - Green spaces rendered in pure green (`#00FF00`)
6. **Rivers** - Waterways rendered in pure blue (`#0000FF`)
7. **Water** - Water bodies rendered in pure blue (`#0000FF`)
8. **Roads** - Secondary roads in white (`#FFFFFF`)
9. **Railroads** - Railway lines in white (`#FFFFFF`)
10. **Highways** - Major roads in yellow (`#FFFF00`)
11. **Buildings** - Individual building footprints in darker lilac (`#A060A0`)

### Mask Colors (Before Watercolor Processing)

- **Land**: `#C4A574` (tan/beige)
- **Water/Rivers**: `#0000FF` (pure blue)
- **Parks**: `#00FF00` (pure green)
- **Roads**: `#FFFFFF` (pure white)
- **Railroads**: `#FFFFFF` (pure white) - railway lines
- **Highways**: `#FFFF00` (pure yellow)
- **Urban**: `#C080C0` (lighter lilac) - landuse areas only
- **Civic**: `#C080C0` (lighter lilac) - civic areas, own layer/texture
- **Buildings**: `#A060A0` (darker lilac) - individual building footprints

---

## Zoom Level 5-7 (Scale: 20M - 4M)

**Continental Scale - Minimal Detail**

### Land

- ✅ Tan/beige background
- All zoom levels

### Water Bodies

- ✅ Lakes and inland water from OSM; the open sea from the processed water polygons
- No zoom-based filtering

### Rivers

- ✅ Major rivers only (2px width)
- No zoom-based filtering

### Parks/Green Spaces

- ✅ Major parks and forests
- No zoom-based filtering

### Highways (Yellow)

- ✅ **Motorway** (3.0px)
- ❌ All other roads excluded

### Roads (White)

- ❌ All roads excluded

### Urban Areas

- ❌ No urban areas at z5-7

### Civic Areas

- ❌ No civic areas at z5-7

### Railroads

- ❌ No railroads at z5-8

### Buildings

- ❌ No individual buildings at z5-7

---

## Zoom Level 8-9 (Scale: 4M - 1M)

**Country/Region Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ Lakes and inland water from OSM; the open sea from the processed water
  polygons, which OSM does not carry (configure `ocean:`, see README)

### Rivers

- ✅ Major rivers (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, nature reserves, and heath areas (includes Lüneburger Heide)

### Highways (Yellow)

- ✅ **Motorway** (4.0px)

### Roads (White)

- ✅ **Trunk** (3.5px)
- ✅ **Primary** (3.0px)

### Urban Areas

- ❌ No urban areas at z8-9

### Civic Areas

- ❌ No civic areas at z8-9

### Railroads

- ✅ **z9+**: Main rail lines (`railway=rail`)

### Buildings

- ❌ No buildings at z8-9

---

## Zoom Level 10-11 (Scale: 1M - 150k)

**Province/State Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (4.5px)

### Roads (White)

- ✅ **Trunk** (4px)
- ✅ **Primary** (3.5px)

### Urban Areas

- ✅ **z11+**: Urban landuse areas (residential, commercial, industrial, retail)
- Helps identify towns and built-up areas

### Civic Areas

- ❌ Civic areas not shown until z14+

### Railroads

- ✅ Main rail lines (`railway=rail`)

### Buildings

- ❌ Individual building footprints not shown until z16+

---

## Zoom Level 12 (Scale: 150k - 75k)

**County/District Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (4.5px)

### Roads (White)

- ✅ **Trunk** (4.5px)
- ✅ **Primary** (4.0px)
- ✅ **Secondary** (3.5px)

### Urban Areas

- ✅ Urban landuse areas (residential, commercial, industrial, retail)

### Civic Areas

- ❌ Civic areas not shown until z14+

### Railroads

- ✅ Main rail lines (`railway=rail`)

### Buildings

- ❌ Individual building footprints not shown until z16+

---

## Zoom Level 13 (Scale: 75k - 50k)

**City Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (5.0px)
- ✅ **Trunk** (4.5px) - _Graduates to highways layer_

### Roads (White)

- ✅ **Primary** (4.5px)
- ✅ **Secondary** (3.5px)
- ✅ **Tertiary** (3.0px)

### Urban Areas

- ✅ Urban landuse areas (residential, commercial, industrial, retail)

### Civic Areas

- ❌ Civic areas not shown until z14+

### Railroads

- ✅ Main rail lines (`railway=rail`)

### Buildings

- ❌ Individual building footprints not shown until z16+

---

## Zoom Level 14 (Scale: 50k - 25k) ⭐

**Urban Area Scale - Current Focus**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (6.5px)
- ✅ **Trunk** (5.5px)
- ✅ **Primary** (5.0px) - _Graduates to highways layer_

### Roads (White)

- ✅ **Secondary** (4.8px)
- ✅ **Tertiary** (3.8px)
- ❌ **Residential** - _Removed to reduce clutter_
- ❌ **Unclassified** - _Removed to reduce clutter_
- ❌ **Living Street** - _Removed to reduce clutter_
- ❌ **Service roads, tracks, paths** - _Removed to reduce clutter_

### Urban Areas

- ✅ Urban landuse areas (residential, commercial, industrial, retail)

### Civic Areas

- ✅ Civic areas (schools, hospitals, universities, colleges, libraries, town halls, stadiums)

### Railroads

- ✅ Main rail lines (`railway=rail`)

### Buildings

- ✅ Individual building footprints (darker lilac `#A060A0`)

**Design Note**: At z14, local streets are hidden to provide a clean regional navigation view. Only major through-roads (secondary and above) are shown.

---

## Zoom Level 15 (Scale: 25k - 3k)

**Neighborhood Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (8.0px)
- ✅ **Trunk** (7.0px)
- ✅ **Primary** (6.0px)
- ✅ **Secondary** (5.0px) - _Graduates to highways layer_

### Roads (White)

- ✅ **Tertiary** (4.0px)
- ✅ **Residential** (3.0px) - _Returns at this zoom_
- ❌ **Unclassified** - _Still excluded_
- ❌ **Living Street** - _Still excluded_
- ❌ **Service roads, tracks, paths** - _Still excluded_

### Urban Areas

- ✅ Urban landuse areas (residential, commercial, industrial, retail)

### Civic Areas

- ✅ Civic areas (schools, hospitals, universities, colleges, libraries, town halls, stadiums)

### Railroads

- ✅ Main rail lines (`railway=rail`)

### Buildings

- ✅ Individual building footprints (darker lilac `#A060A0`)

---

## Zoom Level 16 (Scale: 25k - 3k)

**Local Street Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (8.0px)
- ✅ **Trunk** (7.0px)
- ✅ **Primary** (6.0px)
- ✅ **Secondary** (5.0px)

### Roads (White)

- ✅ **Tertiary** (4.0px)
- ✅ **Residential** (3.0px)
- ✅ **Unclassified** (3.0px) - _Returns at this zoom_
- ❌ **Living Street** - _Still excluded_
- ❌ **Service roads, tracks, paths** - _Still excluded_

### Urban Areas

- ❌ Urban landuse areas not queried at z16+ (individual buildings replace them)

### Civic Areas

- ✅ Civic areas (schools, hospitals, universities, colleges, libraries, town halls, stadiums)

### Railroads

- ✅ Main rail lines (`railway=rail`)
- ✅ Light rail (`railway=light_rail`)

### Buildings

- ✅ Individual building footprints (darker lilac `#A060A0`)

---

## Zoom Level 17 (Scale: 25k - 3k)

**Detailed Street Scale**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ Parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (8.0px)
- ✅ **Trunk** (7.0px)
- ✅ **Primary** (6.0px)
- ✅ **Secondary** (5.0px)

### Roads (White)

- ✅ **Tertiary** (4.0px)
- ✅ **Residential** (3.0px)
- ✅ **Unclassified** (3.0px)
- ✅ **Living Street** (3.0px) - _Returns at this zoom_
- ❌ **Service roads, tracks, paths** - _Still excluded_

### Urban Areas

- ❌ Urban landuse areas not queried at z16+ (individual buildings replace them)

### Civic Areas

- ✅ Civic areas (schools, hospitals, universities, colleges, libraries, town halls, stadiums)

### Railroads

- ✅ Main rail lines (`railway=rail`)
- ✅ Light rail (`railway=light_rail`)
- ✅ Subway and tram (`railway=subway`, `railway=tram`)

### Buildings

- ✅ Individual building footprints (darker lilac `#A060A0`)

---

## Zoom Level 18 (Scale: <3k)

**Building-Level Detail**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ Rivers and streams (2px width)

### Parks/Green Spaces

- ✅ All parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (14.0px)
- ✅ **Trunk** (12.0px)
- ✅ **Primary** (11.0px)
- ✅ **Secondary** (9.6px)

### Roads (White)

- ✅ **Tertiary** (7.6px)
- ✅ **Residential** (4.0px)
- ✅ **Unclassified** (4.0px)
- ✅ **Living Street** (4.0px)
- ❌ **Service roads, tracks, paths** - _Still excluded at z18_

### Urban Areas

- ❌ Urban landuse areas not queried at z16+ (individual buildings replace them)

### Civic Areas

- ✅ Civic areas (schools, hospitals, universities, colleges, libraries, town halls, stadiums)

### Railroads

- ✅ Main rail lines (`railway=rail`)
- ✅ Light rail (`railway=light_rail`)
- ✅ Subway and tram (`railway=subway`, `railway=tram`)

### Buildings

- ✅ All individual building footprints (darker lilac `#A060A0`)

---

## Zoom Level 19+ (Scale: <3k)

**Maximum Detail - All Elements**

### Land

- ✅ Tan/beige background

### Water Bodies

- ✅ All water bodies

### Rivers

- ✅ All rivers and streams (2px width)

### Parks/Green Spaces

- ✅ All parks, forests, green spaces

### Highways (Yellow)

- ✅ **Motorway** (14.0px)
- ✅ **Trunk** (12.0px)
- ✅ **Primary** (11.0px)
- ✅ **Secondary** (9.6px)

### Roads (White)

- ✅ **Tertiary** (7.6px)
- ✅ **Residential** (4.0px)
- ✅ **Unclassified** (4.0px)
- ✅ **Living Street** (4.0px)
- ✅ **All Other Roads** (3.2px) - _Service, track, path, footway, cycleway, etc._

### Urban Areas

- ❌ Urban landuse areas not queried at z16+ (individual buildings replace them)

### Civic Areas

- ✅ Civic areas (schools, hospitals, universities, colleges, libraries, town halls, stadiums)

### Railroads

- ✅ Main rail lines (`railway=rail`)
- ✅ Light rail (`railway=light_rail`)
- ✅ Subway and tram (`railway=subway`, `railway=tram`)

### Buildings

- ✅ All individual building footprints (darker lilac `#A060A0`)

**Note**: At z19+, the catch-all rule renders ALL highway types including service roads, tracks, paths, footways, and cycleways for maximum detail.

---

## Progressive Disclosure Strategy

### Zoom 5-9: Continental/Regional View

- Only the most critical infrastructure (motorways, major trunk roads)
- Basic geography (land, water, major parks)

### Zoom 10-13: City/District View

- Major road network expands progressively
- Primary → Secondary → Tertiary roads appear
- Trunk roads graduate to highways layer at z13

### Zoom 14: Urban Navigation (⭐ Special Level)

- **Clean navigation focus**: Local streets removed
- Only through-roads shown (secondary and above)
- Primary roads graduate to highways layer
- Designed for regional wayfinding without clutter

### Zoom 15-17: Neighborhood Detail

- Local streets return progressively:
  - z15: Residential streets
  - z16: Unclassified roads
  - z17: Living streets
- Secondary graduates to highways layer at z15

### Zoom 18+: Maximum Detail

- All road types visible
- z19+: Even service roads, paths, and tracks appear
- Full building and civic area detail

---

## Configuration Files

- **Layer Definitions**: `assets/styles/layers/*.xml`
  - `land.xml` - Tan/beige background
  - `water.xml` - Water bodies (blue)
  - `rivers.xml` - Rivers and streams (blue)
  - `parks.xml` - Green spaces (green)
  - `roads.xml` - Secondary roads (white)
  - `railroads.xml` - Railway lines (white)
  - `highways.xml` - Major roads (yellow)
  - `buildings.xml` - Individual buildings (darker lilac)
  - `civic.xml` - Civic areas (lighter lilac)

- **Pipeline Processing**: `internal/pipeline/generator.go`
  - Compositing order: Land → Urban → Civic → Parks → Rivers → Water → Roads → Railroads → Highways → Buildings

- **Watercolor Effects**: `internal/watercolor/processor.go`
  - Inset shadow effects
  - Edge darkening
  - Texture blending

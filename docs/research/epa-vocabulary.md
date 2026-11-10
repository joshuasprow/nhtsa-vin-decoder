# How EPA's vehicles.csv lands on vPIC 2026_10

Research for [#144](https://github.com/joshuasprow/nitrovin/issues/144) on
[the vocabulary map](https://github.com/joshuasprow/nitrovin/issues/117). The
question: how many of EPA's fuel-economy names resolve to vPIC 2026_10 makes
and models by exact match after nitrovin's normalization, and how much each
rule a builder might apply adds. The answer sets the EPA builder's rules and
the size of its curated override file. Words are
[`GLOSSARY.md`](../../GLOSSARY.md)'s; the mapping model is
[ADR 0009](../adr/0009-what-a-vocabulary-mapping-says.md).

## Answer

- **Entries.** `vehicles.csv` has 50,647 rows and 5,820 distinct
  `(make, model)` pairs. Normalization (trim, collapse whitespace, Unicode
  lower-case; [ADR 0011](../adr/0011-vocabularies-are-json-lines-loaded-once.md))
  merges 72 case and spacing variants, leaving **5,747 entries** under 145
  makes. The builder has to merge these itself. Two lines for one entry are a
  load fault.
- **Exact match alone** resolves **736 entries (12.8%)**, covering 27.3% of
  rows. 84 of EPA's 145 makes match a vPIC make exactly, and those makes hold
  5,535 entries.
- **Applied in the ticket's order**, the rules reach 81.0% of entries.
  baseModel gives by far the biggest gain (+3,593). Drive suffixes, maker
  drive names, parentheticals, trailing qualifiers, the make spelling table and
  punctuation/space-insensitive matching add the rest. Three more rules reach
  89.7%: the longest vPIC model name that is a token prefix, make-name prefix,
  and slash alternatives.
- **Several.** Matching by name gives at most one model per entry, because
  vPIC has no case-folded model-name collisions within a make and the rules
  prefer the most specific candidate. Only slash alternatives (`Impala/Caprice`)
  resolve to several: 50 entries.
- **Recommended rule set.** Use the cascade in
  [Recommended rules](#recommended-rules), with prefix-versus-baseModel
  disagreements sent to curation. It resolves **5,125 entries (89.2%; 90.9% of
  rows)**, 5,075 to one model and 50 to several. On a hand-checked uniform
  sample, **2 of 119 are wrong (1.7%, 95% CI 0.5–5.9%)**, so expect about
  85 wrong mappings, mostly too coarse (`Sierra LTD` landing on `Sierra`
  instead of `Sierra Limited`).
- **Curated override size.** **501 entries in 257 clusters** have a vPIC make
  and no model; the 50 largest clusters cover 271 of them. Another 121 entries
  under 36 makes have no vPIC make at all (importers, coachbuilders, grey-market
  converters). Those entries need extensions or stay unmapped with a note,
  rather than overrides.
- **Qualifiers.** Model names imply Drive Type for 2,313 entries and Series for
  165 Chevrolet/GMC/Dodge entries. Drive Type is element 15, attribute ids
  1/2/3/4/7. Series is element 34, text. vPIC decodes many 2WD trucks and
  SUVs as `4x2` (7), not FWD or RWD, and decodes Toyota's AWD as `4WD` (2). A
  qualifier's values therefore have to come from what vPIC emits for each
  model, not from a fixed table.
- **Cadence.** EPA republishes `vehicles.csv` every two to four weeks,
  irregularly: 29 distinct `modifiedOn` dates between 2025-01-27 and
  2026-11-03. 236 entries are new in the last 12 months.
- **Licence.** EPA's data.gov listing for FuelEconomy.gov names the
  [EPA Data License](https://edg.epa.gov/EPA_Data_License.htm): public domain
  under 17 U.S.C. § 105. fueleconomy.gov's own site disclaimer, written by its
  contractor ORNL, says documents from the server "may be freely distributed
  and used for non-commercial, scientific and educational purposes". See
  [Licence and terms](#licence-and-terms).

## Inputs

- EPA: `vehicles.csv` from
  [vehicles.csv.zip](https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip),
  fetched 2026-11-10 (`Last-Modified: Wed, 04 Nov 2026 14:31:23 GMT`). It has
  50,647 rows and model years 1984–2027. The fields used:
  - `make`: "manufacturer (division)"
  - `model`: "model name (carline)"
  - `baseModel`: "base model name"
  - `year`, `drive`, `createdOn`, `modifiedOn`

  The field descriptions are quoted from
  [the column documentation](https://www.fueleconomy.gov/feg/ws/index.shtml#vehicle).
- vPIC: the 2026_10 artifact embedded in `nitrovin-data` v1.202610.1:
  - `lookup` (`make`, `model`, `drivetype`): 12,387 makes and 32,070 models.
  - `make_model`: each model sits under exactly one make.
  - `pattern` rows for Model (28), Drive Type (15) and Series (34), joined
    through `wmi_vinschema` year ranges. These give each model's decodable
    years and the Drive Type and Series values its VIN schemas emit.
    31,528 models have at least one Model pattern.

## Method

An entry is a normalized `(make, model)` pair. Its mappings are scoped by the
EPA years it spans; years are never part of the name (ADR 0009). An entry's
make resolves to the vPIC make with the same normalized name, or through the
make spelling table. Its model then resolves within that make through an
ordered cascade of candidate names. The first tier that hits wins. Within a
tier, the longest (least-stripped) candidate wins, so `Stanza Wagon 4WD` lands
on `Stanza Wagon`, not `Stanza`.

The candidate rules:

| Rule | What it does | Example |
| --- | --- | --- |
| baseModel | try EPA's `baseModel` value(s) | `E400` (base `E-Class`) → `E-Class` |
| drive | drop the tokens `2WD 4WD AWD FWD RWD 4x4 4x2` | `Tucson AWD` → `Tucson` |
| maker drive names | drop `4MATIC(+) 4MOTION xDrive sDrive quattro ALL4 SH-AWD E-Four` | `Q8 quattro` → `Q8` |
| parentheticals | drop `(…)` | `EX90 (21 Inch Wheels)` → `EX90` |
| trailing qualifiers | drop a trailing body or fuel word, repeatedly: `Wagon Pickup Van FFV Hybrid Coupe Convertible Sedan Cabriolet Roadster Cab Chassis Hatchback CNG PHEV MHEV EV Electric …` | `Explorer 4WD FFV` → `Explorer` |
| make spelling table | 25 EPA make spellings → vPIC make ids, plus one model prefix (Scion → Toyota `Scion …`) | `McLaren Automotive` → `McLaren` |
| punctuation/space-insensitive | compare with spaces, `-`, `_`, `.` and `'` removed (not `/`) | `F150` → `F-150`, `GR 86` → `GR86` |
| longest prefix | the longest vPIC model name under the make that is a whole-token prefix of the cleaned EPA model | `Range Rover Sport P400 MHEV` → `Range Rover Sport` |
| make-name prefix | try `<make> <model>`, or strip a leading make name; skip vPIC models named like the make | `Mazda 3` → `Mazda3`, `Polestar 2` → `Polestar 2` |
| slash alternatives | split the EPA model on `/` and resolve each part | `Impala/Caprice` → `Impala` + `Caprice` |

Precision was checked by hand. I read each sampled EPA entry, its years and
its baseModel against the vPIC model it landed on and the other vPIC models
under that make, and counted a mapping wrong when a more specific vPIC model
existed or the vehicle was a different one. Two automatic signals were also
computed:

- *coarse*: a longer vPIC model name under the make is a token prefix of the
  cleaned EPA model, so a more specific model was available.
- *year gap*: no year of the EPA entry is among the vPIC model's decodable
  years. This signal is weak before about 1990, where vPIC's patterns are
  thin.

## Per-stage results

The rules are added cumulatively in the ticket's order, then the three extra
rules. Counts are entries (of 5,747). *rows* is the share of the 50,647 EPA
rows those entries carry. In every stage, model-derived candidates (exact,
then stripped) are tried before baseModel candidates.

| Stage (cumulative) | one model | several | none | resolved | rows | coarse | hand check: wrong / sampled |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| exact | 736 | 0 | 5,011 | 12.8% | 27.3% | 0 | 0 / 25 |
| + baseModel | 4,329 | 0 | 1,418 | 75.3% | 79.0% | 131 | 4 / 60 of its gain |
| + drive suffixes | 4,338 | 0 | 1,409 | 75.5% | 79.3% | 78 | 0 / 50 for the four strippers together |
| + maker drive names | 4,369 | 0 | 1,378 | 76.0% | 79.6% | 72 | ″ |
| + parentheticals | 4,378 | 0 | 1,369 | 76.2% | 79.7% | 65 | ″ |
| + trailing qualifiers | 4,520 | 0 | 1,227 | 78.6% | 82.4% | 49 | ″ |
| + make spelling table | 4,564 | 0 | 1,183 | 79.4% | 82.8% | 49 | 0 / 70 (all of its hits) |
| + punctuation/space-insensitive | 4,657 | 0 | 1,090 | 81.0% | 85.1% | 49 | 0 / 25 |
| + longest prefix | 5,018 | 0 | 729 | 87.3% | 89.0% | 0 (by construction) | 1 / 70 |
| + make-name prefix | 5,064 | 0 | 683 | 88.1% | 89.6% | 0 | 0 / 46 (all) |
| + slash alternatives | 5,105 | 50 | 592 | 89.7% | 91.3% | 0 | 1 / 30 wrong, 3 / 30 partial |

Each rule on its own over exact, in entries gained (rows gained in brackets):

| Rule alone | entries | rows |
| --- | ---: | ---: |
| baseModel | +3,593 | +26,175 |
| longest prefix | +3,609 | +26,699 |
| all four strippers | +1,393 | +15,526 |
| drive suffixes | +599 | +9,365 |
| trailing qualifiers | +332 | +3,159 |
| slash alternatives | +104 | +1,015 |
| parentheticals | +65 | +140 |
| maker drive names | +60 | +535 |
| make-name prefix | +29 | +59 |
| make spelling table | +28 | +132 |
| punctuation/space-insensitive | +18 | +118 |

What the checks found:

- **baseModel is coarse when tried first.** EPA gives every Range Rover
  variant base `Range Rover`, every Santa Fe Sport base `Santa Fe`, and the
  2004 `Pathfinder Armada` base `Pathfinder`. vPIC has `Range Rover Sport`,
  `Santa Fe Sport` and `Armada`, so taking baseModel first maps these one
  level too high: 4 of 60 sampled, and 131 entries flagged coarse. Trying
  stripped model names first, then the longest prefix, removes all of them.
  baseModel stays essential after that. It carries Mercedes-Benz (`E350` →
  `E-Class`; vPIC names classes, EPA names engines), the GM `C15`/`K15`
  codes and others: 518 entries resolve at the baseModel step.
- **Longest prefix is the single strongest rule.** It reproduces vPIC's model
  grain from EPA's trim-laden names (`Silverado C15 XFE 2WD` → `Silverado`,
  `M4 Competition M xDrive Convertible` → `M4`). It fails where EPA puts a
  trim word before the model: `Cooper S Countryman` → `Cooper` when vPIC has
  `Countryman`. The guard in the recommended rules catches these.
- **Punctuation folding must not remove `/`.** Folding it turned `Golf/GTI`
  and `New Golf` into vPIC `Golf GTI`, which is wrong for `New Golf`.
  Leaving `/` to the slash rule fixes this.
- **Make-name prefix must skip vPIC models named like their make.** Without
  that, `CX Automotive XM`, `Vector W8` and `Kandi K27` land on vPIC models
  called `CX Automotive`, `Vector` and `KANDI`: 11 wrong.
- **Slash alternatives are the least precise rule.** Parts that vPIC lacks
  drop out silently, so `960/S90` gets only `S90` and `Truck/T100` gets only
  `T100`. One part also landed too coarse: `Lumina/APV Minivan` → `Lumina`
  instead of `Lumina APV`.
- **Year gap.** 132 resolved entries share no year with their vPIC model.
  Most are early cars vPIC barely decodes (`Pontiac 2000 Sunbird` 1984,
  `Ferrari F40` 1990) and were correct when checked. The signal is useful for
  review but not as a rule.

## Recommended rules

For each entry, in order, first hit wins:

1. Make: the exact vPIC make name, else the make spelling table (25 rows,
   below).
2. Model, exact.
3. Model with drive suffixes, maker drive names, parentheticals and trailing
   qualifiers stripped, applied repeatedly; prefer the least-stripped
   candidate.
4. Longest vPIC model name that is a whole-token prefix of a step 3 candidate.
5. baseModel, exact, then stripped as in step 3.
6. Steps 2–5 again, punctuation/space-insensitive (without `/`).
7. Make-name prefix (`Mazda3`, `Polestar 2`, `Ferrari 456 GT` → `456`),
   skipping vPIC models named like the make.
8. Slash alternatives: resolve each `/` part through steps 2–7 and emit one
   mapping per distinct model.
9. **Guard.** If step 4 hit, baseModel also hits a different model exactly,
   and that model's name is not a prefix of the step 4 hit, do not map. Send
   the entry to curation. This catches 30 entries: 15 MINI (`Cooper S Clubman`,
   `Cooper Countryman All4`, `Cooper Paceman`), 10 `New Range Rover …`
   (vPIC has a model named `New Range Rover`), `Civic Del Sol`, `Alpina XB7`,
   and three Aston Martin `V8/V12 Vantage` entries. The Aston Martin prefix
   hits were right, so the guard gives those up for curation.

Mark every derived mapping `origin: derived`. Mark mappings from step 8
`origin: derived` with a note that they come from a slash entry, because they
are the least precise.

Expected result on today's file: **5,125 entries mapped (89.2%), 90.9% of
rows**. 5,075 resolve to one model and 50 to several (all slash entries).
**Error rate ≈ 1.7%** (2 wrong of 119 sampled, 95% CI 0.5–5.9%). Both errors
in the uniform sample are too coarse: `MINI Cooper C Convertible` →
`Cooper` instead of `Cooper Convertible`, and `GMC Sierra LTD K15` → `Sierra`
instead of `Sierra Limited`. The 120-entry uniform sample had a third error,
`MINI Cooper S Clubman` → `Cooper`, which the guard now removes.

The make spelling table (EPA → vPIC make id):

| EPA make | vPIC make (id) |
| --- | --- |
| McLaren Automotive | McLaren (2236) |
| Saleen Performance | Saleen (4355) |
| Sterling | Sterling Motor Car (5555) |
| INEOS Automotive | INEOS (12360) |
| Bugatti Rimac | Bugatti (454) |
| American Motors Corporation | American Motors (1124) |
| RUF Automobile, RUF Automobile GmbH | RUF (12550) |
| Avanti Motor Corporation | Avanti (5552) |
| Panoz Auto-Development | Panoz (4220) |
| CODA Automotive | Coda (1777) |
| London Taxi, London Coach Co Inc | LondonCoach Inc (5767) |
| Excalibur Autos | Excalibur Automobile Corporation (7477) |
| Consulier Industries Inc | Consulier (10249), Consulier GTP (5557) |
| LaForza Automobile Inc | LAFORZA (6307) |
| Panther Car Company Limited | Panther (5938) |
| ASC Incorporated | ASC, INC. (1194) |
| SRT | Dodge |
| BMW Alpina | BMW |
| VPG, Mobility Ventures LLC | AM General (vPIC's make `Mobility Ventures` has no models; `MV-1` sits under AM General) |
| Roush Performance | Ford |
| Scion | Toyota, model prefixed `Scion ` (`xB` → `SCION xB`) |
| STI | Subaru |

## Curated override size

After the recommended rules, 622 entries (4,611 rows) remain unmapped.

- **121 entries under 36 makes have no vPIC make.** The largest are J.K.
  Motors 21, Wallace Environmental 18, Federal Coach 14, Import Trade
  Services 11, Tecstar 6, and TVR, Kenyon, McEvoy and Texas Coach with 4
  each. Most are 1980s–90s grey-market converters and coachbuilders whose
  vehicles are other makes' cars. They become extensions or unmapped entries
  with notes, not overrides.
- **501 entries (4,438 rows) have a vPIC make but no model.** They form 257
  clusters by `(make, baseModel)`. 184 clusters are singletons. The top 10
  cover 118 entries, the top 50 cover 271, and the top 100 cover 344.

The largest clusters, which are the first override work:

| Make | baseModel | entries | rows | Likely target or cause |
| --- | --- | ---: | ---: | --- |
| Mazda | B Pickup | 25 | 213 | `B2300/B3000/B4000` → vPIC `B-Series` |
| Nissan | ARIYA | 16 | 25 | vPIC splits `Ariya Hatchback` / `Ariya MPV` |
| Mercedes-Benz | EQE | 12 | 24 | `EQE-Class Sedan` / `EQE-Class SUV` |
| Mercedes-Benz | EQS | 11 | 34 | `EQS-Class Sedan` / `EQS-Class SUV` |
| MINI | John Cooper Works | 10 | 61 | trim; vPIC has no JCW model |
| Land Rover | Range Rover | 10 | 16 | guard: `New Range Rover …` |
| GMC | Pickup | 9 | 254 | `C15/K15 Pickup` → vPIC `C/K Pickup`, Sierra |
| Volvo | 740/760 | 9 | 98 | vPIC `740 Series` / `760 Series` |
| MINI | Countryman | 8 | 89 | guard |
| Volvo | 940 | 8 | 21 | `940 Series` |
| BMW | 3 Series | 7 | 62 | model codes vPIC lacks (`323ci`, `328cxi`, `335ci`) |
| Rolls-Royce | Brooklands/Turbo R | 7 | 14 | Bentley cars filed under Rolls-Royce; vPIC has Bentley `Brooklands`, `Turbo` |
| Volvo | 240 | 6 | 82 | `240 Series` |
| Subaru | Sedan | 6 | 63 | 1980s generic body names |
| Mercedes-Benz | CLE-Class | 6 | 23 | vPIC `CLE` |
| BMW | 7 Series | 6 | 18 | model codes vPIC lacks (`740Ld xDrive`, `ActiveHybrid 7`) |
| Mercedes-Benz | SLS AMG | 6 | 14 | vPIC `SLS-Class` |
| Nissan | Truck | 5 | 96 | 1980s `Truck`; vPIC `Pickup` |
| Chevrolet | Sprint | 5 | 29 | vPIC `Geo Sprint` |
| Chevrolet | Sport Van | 5 | 26 | G-van; vPIC `G-Series` |
| Mitsubishi | Nativa | 5 | 23 | EPA marks it Puerto Rico only; vPIC `Montero Sport` likely |
| Chevrolet | Van 1500 | 5 | 17 | G-van; vPIC `G-Series` |
| Chevrolet | Astro Cargo | 4 | 73 | `Astro Van` |
| MINI | Clubman | 4 | 56 | guard |
| American Motors Corporation | Eagle | 4 | 27 | vPIC `Eagle 30`/`Eagle 50` |

**Estimate: about 500 curated lines, of which about 270 come from the 50
largest clusters**, where one decision often covers a whole cluster (the
`-Class`, `… Series` and sedan/SUV splits above). The remaining
~230 are mostly singletons that need one decision each. Another ~120 lines are
extensions or unmapped entries for the makes vPIC lacks. New EPA entries add
little: of the 236 entries created in the last 12 months, 231 resolve by rule.

## Qualifiers EPA's names imply

### Drive Type: element 15, lookup `DriveType`

2,313 entries (22,739 rows) carry a drive token. EPA's `drive` column says
the same thing more reliably and is present on 49,461 rows, so a builder
should read the column and use the model-name token only as a fallback.

| EPA column `drive` | rows | EPA name tokens |
| --- | ---: | --- |
| Front-Wheel Drive | 15,933 | FWD |
| Rear-Wheel Drive | 15,896 | RWD, most 2WD |
| All-Wheel Drive | 6,953 | AWD, xDrive, ALL4, 4MOTION, quattro |
| 4-Wheel or All-Wheel Drive | 6,645 | 4WD, AWD, 4MATIC, quattro |
| 4-Wheel Drive | 2,791 | 4WD, 4MATIC |
| Part-time 4-Wheel Drive | 736 | 4WD |
| 2-Wheel Drive | 507 | 2WD |
| (empty) | 1,186 | |

vPIC's attribute ids (2026_10 `lookup.drivetype`), and what the patterns of
the mapped models actually emit:

| vPIC id | vPIC value | Holds for EPA |
| ---: | --- | --- |
| 1 | FWD/Front-Wheel Drive | FWD, 2WD on front-drive cars |
| 2 | 4WD/4-Wheel Drive/4x4 | 4WD, 4x4; also AWD where vPIC codes it so (Toyota `RAV4`, `Tacoma`, `Sienna`) |
| 3 | AWD/All-Wheel Drive | AWD and the maker drive names (`X5`, `E-Class`, `A4`, `Tiguan`, `Countryman` all decode 3) |
| 4 | RWD/Rear-Wheel Drive | RWD, 2WD |
| 7 | 4x2 | 2WD on trucks and SUVs: `F-150`, `Silverado`, `Sierra`, `1500`, `Tacoma`, `RAV4` and `Sienna` decode 7, not 1 or 4 |
| 24 | 2WD/4WD | rare, ambiguous |

So `2WD` → {7, 4, 1} and `4WD`/`AWD` → {2, 3}. Narrow each mapping to the
values that model's VIN schemas emit, which can be computed from `pattern`
rows (element 15) joined to the model's schemas, as this research did.
Otherwise the qualifier names values the decode never produces, which is
drift under ADR 0011.

### Series: element 34, text

165 entries imply a tonnage or GM chassis code: 78 Chevrolet, 77 GMC and
10 Dodge, carrying `1500`, `2500`, `3500` or `C10/K10/C15/K15/C20/K25/R15/V10`
and similar. In GM codes the letter also implies Drive Type: C/R = 2WD (7),
K/V = 4WD (2). How vPIC spells Series differs by make:

| vPIC model (id) | Series values in its patterns (most frequent) | Use |
| --- | --- | --- |
| Chevrolet `Silverado` (1850) | `1500`, `2500`, `3500`, `1/2 Ton`, `1 Ton`, `3/4 Ton`, `1500 (1/2 Ton)`, `1/2 ton`, `1500 Conversion`, … | Series qualifier, listing every spelling |
| GMC `Sierra` (1857) | `1500`, `2500`, `3500`, `1500 (1/2 Ton)`, `2500 (3/4 Ton)`, `3500 (1 Ton) Luxury`, `1/2 Ton`, … | same |
| `Silverado HD` (24906), `Sierra HD` (24909) | `2500`, `3500` | model already separates HD |
| Chevrolet `Suburban` (1851) | `1/2 Ton`, `3/4 Ton`, `HD`, `1500`, `2500`, … | same |
| Dodge `Ram` (1938), to 2012 | `1500`, `2500`, `3500`, `3500W`, `DR (1500)`, `D1 (3500)`, … | Series qualifier |
| Ram `1500` (13620), `2500` (13621), from 2013 | chassis codes `DT`, `DS`, `DJ`, `Classic (DS)`, … | tonnage is the model; no Series qualifier |
| Ford `F-150` (1801), `F-250` (1805) | `F-Series`, `Super Duty - Single Rear Wheel`, … | tonnage is the model; no Series qualifier |

EPA already splits Dodge `Ram 1500 Pickup` (1994–2012) from make Ram `1500`
(2013–), so year scope needs no rule there. Body words EPA strips
(`Wagon`, `Van`, `Pickup`, `Cab Chassis`, `Convertible`, `Coupe`) map onto
Body Class (element 5, lookup `BodyStyle`) and Cab Type (4). Fuel words
(`FFV`, `CNG`, `Hybrid`, `PHEV`) map onto Fuel Type - Primary/Secondary
(24/66) and Electrification Level (126). These were not measured here.

## Update cadence

The download page and the web-services page both say "Updated: Wednesday
November 04 2026", and the zip's `Last-Modified` header is 2026-11-04
([download page](https://www.fueleconomy.gov/feg/download.shtml)). Inside the
file, `modifiedOn` has 29 distinct dates from 2025-01-27 to 2026-11-03. That
is a republish every two to four weeks, irregular, with 1–395 rows touched
each time. Over the last 12 months 1,554 rows were modified and 236 entries
created, arriving in batches (Feb 36, Mar 20, May 21, Jun 19, Aug 62, Sep 20,
Nov 58), mostly new model years. data.gov's catalog metadata says
`accrualPeriodicity R/P1Y` (yearly) and "Last Updated January 01, 2014",
which is stale. A monthly rebuild, or one per release, catches every change
with little lag. fueleconomy.gov runs a data-users mailing list for
announcements ([web services](https://www.fueleconomy.gov/feg/ws/index.shtml)).

## Licence and terms

- EPA's data.gov entry for
  [www.FuelEconomy.gov](https://catalog.data.gov/dataset/www-fueleconomy-gov)
  (publisher U.S. EPA Office of Air and Radiation, access level `public`)
  names `license: https://edg.epa.gov/EPA_Data_License.htm`.
  [That licence](https://edg.epa.gov/EPA_Data_License.htm) says: "Unless
  otherwise specified, all data produced by the U.S EPA is by default in the
  public domain and is not subject to domestic copyright protection under
  17 U.S.C. § 105."
- fueleconomy.gov's own
  [Privacy/Security disclaimer](https://www.fueleconomy.gov/feg/ORNL-disclaimer.htm),
  written by Oak Ridge National Laboratory, which administers the site, says
  under "Copyright Status": "Documents provided from the web server were
  sponsored by a contractor of the U.S. Government under contract
  DE-AC05-00OR22725. Accordingly, the U.S. Government retains a nonexclusive,
  royalty-free license … These documents may be freely distributed and used
  for non-commercial, scientific and educational purposes." It disclaims
  warranty and endorsement, and the vehicle photographs belong to the
  manufacturers.
- [INFERENCE, not legal advice] The data are EPA's: test results "done at
  the Environmental Protection Agency's National Vehicle and Fuel Emissions
  Laboratory … and by vehicle manufacturers with oversight by EPA" (download
  page), and EPA's catalog licenses them public domain. The ORNL wording
  covers the site's documents. A nitrovin vocabulary also carries only
  make/model strings, which are facts, plus nitrovin's own mappings. Calling
  the published vocabulary public domain looks sound. It should cite EPA's
  licence and carry the no-endorsement disclaimer. Whoever owns the decision
  should note that the site's contractor notice reads "non-commercial".

## Sources

- EPA, [Download Fuel Economy Data](https://www.fueleconomy.gov/feg/download.shtml)
  and [vehicles.csv.zip](https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip),
  fetched 2026-11-10.
- EPA/DOE, [Fuel Economy Web Services: vehicle fields](https://www.fueleconomy.gov/feg/ws/index.shtml#vehicle).
- ORNL, [fueleconomy.gov Privacy/Security disclaimers](https://www.fueleconomy.gov/feg/ORNL-disclaimer.htm).
- data.gov, [www.FuelEconomy.gov](https://catalog.data.gov/dataset/www-fueleconomy-gov);
  EPA, [Standard Open Data License](https://edg.epa.gov/EPA_Data_License.htm).
- vPIC 2026_10 as embedded in `github.com/joshuasprow/nitrovin-data`
  v1.202610.1 (`artifact.Open(embedded.Artifact())`): tables `lookup`,
  `make_model`, `pattern`, `wmi_vinschema`, `element`.

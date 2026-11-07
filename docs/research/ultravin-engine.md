# ultravin's engine and parity harness, inventoried for a Go port

Ticket #12. Sources: ultravin `dbc9777a6baaa41f2138157cdb7bf83e0360fcd8` (2026-11-03,
MIT; cited as `uv:path:line`) and NHTSA's `spVinDecode` as vendored there under
`vpic/procs/` from the 2026_10 dump (cited as `sql:file:line`). Terms are
`GLOSSARY.md`'s. Nothing here is copied source; every table is a reading of it.

## 0. Shape of the thing

| fact | value | source |
|---|---|---|
| Oracle proc | `vpic.spvindecode(v, includeprivate=false, year=null, includeall=null, nooutput=false)` | sql:spvindecode.sql:1 |
| Call shape the harness uses | `select * from vpic.spvindecode(%s)` — defaults only | uv:scripts/parity/oracle.py:49 |
| Engine entry | `decode_full(db, input, now_micros, current_year, caller_year)` — the clock is an input | uv:crates/ultravin/src/lib.rs:1046 |
| Output row | 15 columns: groupname, variable, value, itempatternid, itemvinschemaid, itemkeys, itemelementid, itemattributeid, itemcreatedon, itemwmiid, code, datatype, decode, itemsource, itemtobeqced | sql:spvindecode.sql:1; uv:scripts/parity/normalize.py:19 |
| Engine Rust, decode path only | decode 1339 + errors 1696 + lib ≈1200 of 2986 + matcher 800 + conversion 323 + checkdigit 309 + tables 871 + db 1718 + year 120 + wmi 55 + resolve 42 + keyspec 106 ≈ **8.6k lines** (incl. tests/comments) | `wc -l` on uv:crates/ultravin/src |
| Not decode path (skip in a port) | adaptive, arrow_io, parquet_io, batch_results, native_*, predictor, stage_trace, ids, json, generate (1763), cover (633) ≈ 9k lines of batching/columnar/VIN-generation | same |
| Importer | main 410 + artifact 638 + stalecache 403 = 1451 lines | uv:crates/ultravin-build/src |
| Artifact | rkyv, 64-byte header (magic `ULTRAVIN`, format 1, blake3), **84,124,480 bytes** for 2026_10 | uv:crates/ultravin/src/tables.rs:502-507; uv:vpic/manifest.json |

## 1. The decode pipeline, stage by stage

The wrapper (`spvindecode`) chooses model-year candidates, runs the core once per
candidate ("pass"), scores the passes, keeps one, resolves lookup names, projects.

| # | stage | mirrors (proc:lines) | Rust | notes |
|---|---|---|---|---|
| W0 | sanitize: trim, uppercase; non-ASCII char → `&` | spvindecode.sql:36 (`upper(trim(v))`) | lib.rs:989 `sanitize_into` | byte indexing downstream is only safe on ASCII, hence the substitution |
| W1 | WMI = pos 1-3, +12-14 when pos 3 = `9` and len ≥ 14 | fvinwmi.sql:8-16 | wmi.rs:6 | |
| W2 | descriptor = pad to 17 with `*`, mask pos 9, take 11 (14 if pos 3 = `9`) | fvindescriptor.sql:7-12 | wmi.rs:25, lib.rs:1017 | emitted as element 196 only |
| W3 | `var_keys` = pos 4-8 ‖ `\|` ‖ pos 10-17 (`CM826\|3A004352`) | spvindecode_core.sql:54-59 | decode.rs:291 | the string every pattern key is matched against |
| W4 | pass 1 (descriptor table) — **dead**: `fVinDescriptor(vin)` runs on line 35 before `vin` is assigned on 36, so `dmy` is looked up for `***********`; no `vindescriptor` row has that key (69 rows) | spvindecode.sql:35-54 | lib.rs:1506 skips it | verified on uv:vpic/lookups.json; a port must not "fix" this |
| W5 | year plan: rmy/omy/conclusive (§3) | spvindecode.sql:54-88; fvinmodelyear2.sql | year.rs:72 | |
| W6 | pass 2: caller year, only if in `[1980, v_limit]` and ≠ rmy and ≠ omy; afterwards passes 3/4 run only if pass 2 returned code 8 and rmy exists | spvindecode.sql:91-113 | lib.rs:1514-1540 | `modelYearSource` becomes the year text instead of `***X*\|Y` |
| W7 | pass 3: rmy; pass 4: omy when inconclusive; `e12` = caller year present and ≠ pass year | spvindecode.sql:115-148 | lib.rs:1542-1595 | |
| W8 | score and pick the best pass (§3) | spvindecode.sql:145-188 | lib.rs:1719 `best_pass`, :1742 `score` | |
| W9 | `ItemTobeQCed` from `VinSchema.TobeQCed` for pattern/formula/enginemodel/conversion sources; drop QC'd rows (includeNotPublicilyAvailable is never true) | spvindecode.sql:191-200 | lib.rs:1614 `retain(!to_be_qced)` | the coverage allowance records 0 `tobeqced` schemas in the current dump (uv:scripts/coverage_allowances.json:26), so this is latent |
| W10 | resolve `XXX` values through `fElementAttributeValue`; tabs/CR/LF → space | spvindecode.sql:203-206, :212; felementattributevalue.sql | resolve.rs:17, lib.rs:307 `scrub_value` | lookup hit → name; miss → **empty**, never the id; element with no lookup → raw attribute id |
| W11 | project: elements with non-null `Decode`, not private, only decoded ones (`includeAll` null); order by GroupName CASE rank (27 ranks, else 99) | spvindecode.sql:209-262 | lib.rs:1813, tables.rs:51 `group_rank` | intra-group order is executor order in Postgres — the harness excludes it (§6) |

The core (`spvindecode_core(pass, modelYear, vin, modelYearSource, conclusive, error12, …)`):

| # | stage | mirrors (spvindecode_core.sql) | Rust (decode.rs unless noted) | source label / priority |
|---|---|---|---|---|
| C1 | WMI row: `wmi = var_wmi and PublicAvailabilityDate <= NOW()`; none → code 7, no items | :68-72 | db.rs:366 `wmi_by_str(now)`; `decode_core_into` early return | |
| C2 | pattern pass: for every `Wmi_VinSchema` band containing the year (year null → all bands), schema not QC'd; pattern matches when `keys` has no `[` and `var_keys LIKE replace(keys,'*','_')\|\|'%'`, or has `[` and `var_keys ~ keys_regex`; element ∉ {26,27,29,39}, `Decode` non-null, not private; result ordered by `Pattern.Id` | :75-105 | :374-424; matcher.rs | `Pattern`, priority = band's `YearFrom` |
| C3 | engine model: pick element-18 item by (priority desc, createdon desc, id desc); `lower(trim(name))` match on `EngineModel`; emit its `EngineModelPattern` rows with the parent's pattern id/keys/schema | :107-127 | :426-452 `pick_element18` | `EngineModelPattern`, 50 |
| C4 | vehicle type 39 from `Wmi.VehicleTypeId` | :129-137 | :454-469 | `VehType`, 100 |
| C5 | manufacturer 27 (name) and 157 (id) from `Wmi.ManufacturerId` | :139-143 | :471-499 | `Manu. Name` / `Manu. Id`, 100 |
| C6 | model year 29 = the pass's year; keys = `modelYearSource` | :145-148 | :501-521 | `ModelYear`, 100 |
| C7 | formula patterns: `formulaKeys` = var_keys with digits → `#`; pattern keys containing `#`, element ∉ {26,27,29,39}, `formulaKeys LIKE replace(keys,'*','_')\|\|'%'`; value = slice of var_keys from first to last `#`; **no** Decode/private/QC/availability filter | :150-173 | :567-662 `append_formula_patterns`, `formula_value` | `Formula Pattern`, 100 |
| C8 | dedup: `RANK() OVER (PARTITION BY ElementId ORDER BY Priority DESC, CreatedOn DESC, length(replace(keys,'*','')) ASC, replace(keys,'[',''),']','') ASC, id ASC)` keep rank 1; elements {114,121,129,150,154,155,169,186} exempt | :175-195 | :715-754 `dedup_per_element`, `dedup_cmp`; tables.rs:18 | CreatedOn DESC puts NULLs first in Postgres — ported as such (`created_desc_nulls_first`) |
| C9 | make 26: if an element-28 (model) item exists → every `Make_Model` make for it, priority 1000 (inherits the model item's pattern id/keys/schema); else if the WMI has exactly one `Wmi_Make` → that make, priority −100; **not re-deduped** | :197-229 | :756-808 | `pattern - model` / `Make` |
| C10 | conversion: cursor over items × `conversion` by `FromElementId`, order (priority desc, createdon desc, conversion id); emit `ToElementId` only if absent; formula `#x#` substituted, evaluated as SQL, any error → `'0'` | :232-285 | :819-895; conversion.rs | `Conversion <id>: <formula>` truncated to 50, 100 |
| C11 | vehicle specs: candidate `VSpecSchemaPattern`s by (WMI's makes, vehicle type, model id, year ∈ schema years or no years); keep those whose every `IsKey` row matches a decoded (element, attribute) case-insensitively; emit non-key rows for elements not yet decoded (carve-out {1,114,121,129,150,154,155,169,186}); one per element by latest ChangedOn | :287-378 | :906-1075 `append_vehicle_specs` | `Vehicle Specs`, −100; tie on ChangedOn is undefined in SQL, Rust breaks by (sp id, schema id) desc — a port must match **ultravin** here, and the corpus will say if it ever matters |
| C12 | code 8 if no item carries a PatternId; else run `spvindecode_errorcode` (§4) | :380-388 | errors.rs:1081-1124 | |
| C13 | code 9 (body style 64), 10 (off-road body styles → `isOffRoad`), 11 (year null) | :395-405 | errors.rs:1087-1105, :1126-1136 | |
| C14 | `VinException` check-digit exemption; `startPos` 15 / 13 (car/MPV/LT) / 14; invalid-character scan with four position classes; each bad char stamps `!` into the corrected VIN and appends `, j:c` | :409-449 | errors.rs:115, :1139-1187 | pos 9 skipped when off-road or exempt |
| C15 | code 400 if any invalid char; code 12 if `error12` | :451-457 | errors.rs:1189-1195 | |
| C16 | defaults: `DefaultValue` rows for the vehicle type, elements not yet present; value `Not Applicable` when lookup-typed and default `'0'`, else `XXX` | :459-476 | :1105-1135 (deferred as a bitmask when `defaults_independent`) | `Default`, 10 |
| C17 | code 6 if len < 17 else check digit (`fVINCheckDigit2`) → code 1 unless exempt | :478-485 | errors.rs:1197-1212; checkdigit.rs | `'?'` compares equal to a literal `?` at pos 9 — bug-for-bug |
| C18 | code 0 when the codes minus {9,10,12} are empty or exactly {14}; code 14 when 0 present and no element 28 | :487-500 | errors.rs:1214-1231 | |
| C19 | AdditionalDecodingInfo 156: texts for 4, 5 (from `ErrorCode.additionalerrortext`), 14 (unused positions), 400 (invalid chars), incomplete-vehicle warning (type 10 or 16 body styles), inconclusive-year note; trim, cap 500 | :502-522 | errors.rs:944-989, :1233 | 4/5 texts are hardcoded in Rust (errors.rs:92-94) rather than read from the table |
| C20 | error text 191 = `ErrorCode.Name` joined `; ` for every code, with the off-road note on code 1 and the exception note on code 0; codes 143 = ids joined `,` sorted numerically; corrections 142 (suggested VIN), 144 (error bytes), 196 (descriptor) | :524-566 | lib.rs:2007-2130 | `Corrections`, 999 |

Pass-pruning in Rust (lib.rs:1642-1676: skip a pass whose best possible score
cannot beat an existing one) is an optimisation with a parity test behind it, not
semantics; a port can run every pass.

## 2. Logical schema: every table and column the decoder reads

Row counts are 2026_10 (uv:vpic/manifest.json). Column names are the dump's
(lowercase). `createdon_key = coalesce(updatedon, createdon)` as epoch µs
(uv:crates/ultravin-build/src/artifact.rs:195-233); NULL ints → `i32::MIN`.

| table | rows | columns read | used by |
|---|---|---|---|
| `wmi` | 13,014 | id, wmi, manufacturerid, makeid, vehicletypeid, trucktypeid, publicavailabilitydate, createdon/updatedon | C1, C4, C5, C9, C14, §3 carLT |
| `wmi_vinschema` | 41,810 | id, wmiid, vinschemaid, yearfrom, yearto (NULL → 2999) | C2, C7, §3 altMY, §4 charset |
| `vinschema` | 25,299 | id, tobeqced | C2, W9 |
| `pattern` | 1,682,444 | id, vinschemaid, keys, elementid, attributeid, createdon/updatedon (+ derived `keys_regex`, `has_bracket`) | C2, C7, C8, §4 |
| `element` | 161 | id, name, code, isprivate, groupname, datatype, decode, weight | C2 filter, W8 weights, W11 projection, C19 datatype |
| `make_model` | 32,070 | makeid, modelid | C9 |
| `wmi_make` | 14,191 | wmiid, makeid | C9, C11 |
| `enginemodel` | 346 | id, name | C3 |
| `enginemodelpattern` | 2,510 | id, enginemodelid, elementid, attributeid, createdon/updatedon | C3 |
| `defaultvalue` | 186 | id, elementid, vehicletypeid, defaultvalue, createdon/updatedon | C16 |
| `vinexception` | 18,806 | vin, checkdigit | C14, C17 |
| `conversion` | 6 | id, fromelementid, toelementid, formula | C10 |
| `vehiclespecschema` | 6,619 | id, makeid, vehicletypeid, tobeqced | C11 |
| `vspecschemapattern` | 17,722 | id, schemaid | C11 |
| `vehiclespecpattern` | 231,867 | id, vspecschemapatternid, iskey, elementid, attributeid, createdon/updatedon | C11 |
| `vehiclespecschema_model` | 7,508 | vehiclespecschemaid, modelid | C11 |
| `vehiclespecschema_year` | 12,347 | vehiclespecschemaid, year | C11 |
| 72 lookup tables (`make` 12,387, `model` 32,070, `manufacturer` 23,098, `bodystyle`, `errorcode` 15, …) | 68,235 total | id, name (+ `errorcode.weight`, `additionalerrortext`) | W10 (`fElementAttributeValue`'s 72-arm CASE, sql:felementattributevalue.sql:7-330), C19, C20 |
| `vindescriptor` | 69 | — | read only by the dead pass W4; **omit** |
| `wmiyearvalidchars` | 8,806,153 | wmi, year, position, char | the oracle's §4 charset; ultravin **omits** it and recomputes (§5) |
| `wmiyearvalidchars_cacheexceptions` | 0 | wmi | gate only (§6) |

Not read by the decoder: the NCSA views 96/97/98 (resolve to the raw id,
uv:crates/ultravin/src/tables.rs:85). Total decode-critical rows ≈ 2.2M, of
which `pattern` is 77%.

ultravin's artifact (uv:crates/ultravin/src/tables.rs:452-500): one string arena
(`bytes` + `offsets`, strings referenced by u32 id, interned first-seen over a
fixed table order), then one sorted slice per table — `wmi` by (wmi, id),
`wmi_vinschema` by (wmiid, id), `pattern` by (vinschemaid, id), `make_model` by
(modelid, makeid), `defaultvalue` by (vehicletypeid, id), `lookups` by (tag, id),
etc. Range lookups are binary searches on those orders (db.rs:435-500). The
sort orders are checked on load (tables.rs:605) and make the bytes a pure
function of the dump (blake3 pinned in the manifest). Also stored: `cover`, the
~164 VINs that reach every behaviour token (tables.rs:489; cover.rs).

## 3. Year rules, ranking and tie-breaks

Model year from position 10 (sql:fvinmodelyear2.sql; uv:year.rs:11-51):

| rule | detail |
|---|---|
| map | A-H → 2010-2017, J-N → 2018-2022, P → 2023, R-T → 2024-2026, V-Y → 2027-2030, 1-9 → 2031-2039; I/O/Q/U/Z/0 → null (code 11) |
| carLT | WMI vehicle type ∈ {2, 7} or (3 with truck type 1) — read from **any** `wmi` row by string, availability ignored (year.rs:36, db.rs:371) |
| pos 7 | carLT and pos 7 digit → year − 30, conclusive; carLT and pos 7 letter → conclusive |
| nowYear+2 | year > current + 2 → year − 30, conclusive — runs even for an unknown WMI |
| inconclusive | returned negative; wrapper sets `rmy = \|y\|`, `omy = rmy − 30`, pass 4 runs on omy, note appended to 156 |
| altMY swap (conclusive only) | alt = rmy+30 if rmy ∈ [1980, v_limit−30], or rmy−30 if rmy ∈ [2010, v_limit]; take alt when **no** `wmi_vinschema` band covers rmy and one covers alt (sql:spvindecode.sql:60-83; year.rs:88-101) |
| v_limit | `EXTRACT(YEAR FROM CURRENT_DATE + 2 years)` (sql:spvindecode.sql:26); Rust `current_year + 2` from `now_micros` |
| caller year | pass 2 only when ∈ [1980, v_limit]; equal to rmy or omy → no pass 2; a pass gets code 12 when caller year ≠ its year |

Pass scoring (sql:spvindecode.sql:145-188; uv:lib.rs:1719-1800), tuple compared
descending, lowest pass id on a full tie:

| component | definition |
|---|---|
| ErrorValue | Σ `errorcode.weight` over the pass's codes: 1→−5, 2/3→−100, 4→−200, 5→−300, 6→−10, 7→−10000, 8→−1000, 11/12→−20, 14→−30, 400→−40, else 0 (tables.rs:33; data in uv:vpic/lookups.json `errorcode`) |
| ElementsWeight | Σ `element.weight` over distinct elements with non-empty value (Rust counts deferred defaults too) |
| Patterns | count of items with source ∈ {Pattern, EngineModelPattern, Formula Pattern} and value ∉ {'', 'Not Applicable'} — note at scoring time values are still `XXX`, so effectively every such item |
| ModelYear | element-29 value + 10000 when it equals the caller year; NULLs last |

Within-stage orderings a port must reproduce exactly: pattern items by
`Pattern.Id`; dedup comparator (C8); element-18 pick (C3); conversion cursor
(C10); spec dedup (C11); error-code CSV numeric ascending; output by group rank.

## 4. Error-correction machinery (codes 2-5, 14, 400, suggested VIN)

Runs inside each pass only when some item has a PatternId (C12).
`spvindecode_errorcode(vin, modelYear)` (sql:spvindecode_errorcode.sql; uv:errors.rs:534-676):

| step | SQL | behaviour |
|---|---|---|
| E0 | :36-39 | WMI shorter than 3 → code 6, return (nothing else) |
| E1 | :46-66 | charset = `WMIYearValidChars` rows for (wmi, year) unless the WMI is in `_CacheExceptions`; **if empty**, fall back to `fExtractValidCharsPerWmiYear`: for every distinct `pattern.keys` of every schema whose band covers the year, `fValidCharsInKey` (key index + 3 = VIN position; `#` → 0-9; `*` → nothing in strict mode; `[…]` → the VALIDCHARS members matching `^[…]$`, or the literal list when no `-`/`^`) |
| E2 | :68-104 | n = 14 (11 for a 6-char WMI); for VIN pos 4..min(n, len) except 9 and 10: if the charset has rows at that position and the char is absent → `!`, record `(pos:chars)` with `_` first then sorted, count an error; else keep the char |
| E3 | :106-114 | corrected = WMI + corrected middle (+ last 3 of a 6-char WMI) + up to 3 trailing input chars |
| E4 | :116-145 | exactly one error: one candidate → code **2**, substitute it; several → try each, keep those whose pos 9 equals `fVINCheckDigit` (the v1 function: pos 13 **and** 14 numeric) — exactly one survivor → code **3** with that VIN, else code **4** with the `!` VIN |
| E5 | :147-151 | more than one error → code **5**, `!` VIN, all replacement groups |
| E6 | :163-199 | unused positions: union of `fValidCharsInKey` over every matched item's keys (source ILIKE '%pattern%'); positions 4-8 and 11 whose VIN char is not in that union → code **14** with the list |

Then the core's own scan (C14) stamps `!` over the suggested VIN for invalid
characters and emits 400; element 144 is the error bytes, 142 the suggested VIN
(empty when codes 2-5 did not fire), 156 the explanation.

What feeds it: `pattern.keys` (both E1-fallback and E6), `wmi_vinschema` bands,
`wmi` (length), `vinexception` (E4 is unaffected; C14/C17 are). ultravin builds
the charset per (wmi, year-interval) lazily and memoises it (errors.rs:302-361);
the SQL Server collation order `_` before alphanumerics is reproduced by a type
whose only rendering is that order (errors.rs:37-66).

**ultravin never reads `WMIYearValidChars`.** It always takes the fallback
(errors.rs:400-412). The oracle prefers the table, which is a stale snapshot of
the recompute for 5,706 of its (wmi, year) cells in 2026_10
(uv:scripts/stale_cache_cells.json `summary`). That is the whole source of the
registered deviations (§5). A Go port has the same choice: ship the 8.8M-row
table (≈4× the rest of the artifact) and match the oracle bug-for-bug, or
recompute and inherit ultravin's deviation class. ADR material for #17/#18.

## 5. Deviation registry: shape and expiry

`scripts/known_problems.json` (uv), one entry per VIN; policy in
uv:docs/ACCEPTANCE.md, evidence sections in uv:docs/KNOWN_DEVIATIONS.md.

| field | values / meaning |
|---|---|
| `vin` | the VIN |
| `kind` | `oracle-crash` (the oracle raised; nothing to compare) or `deviation` (both answered, ultravin is right) — uv:scripts/refresh.py:98 |
| `class` | the evidence section's anchor, e.g. `stale-wmiyearvalidchars-cache`, `stale-cache-code-3-vs-5` |
| `scope` | `error-fields` (diff confined to 142/143/144/156/191) or `clean-decode` (the vehicle changes) — refresh.py:99 |
| `cause` | prose naming the defective upstream artifact |
| `first_observed` | data month |
| `evidence` | one-line pointer: the defective cell/row and the rolled-back counterfactual that makes the oracle agree |
| `doc` | `KNOWN_DEVIATIONS.md#<anchor>` |

Admission (ACCEPTANCE "How a divergence is adjudicated"): root-caused to a named
upstream defect; reproducible evidence that is not the output diff; registered
**and** frozen into the corpus so the expected diff shape is locked; re-verified
every refresh. 2026_10 carries 7 entries, all `deviation`, all stale-cache
(2 clean-decode on WMI `10T`, 5 error-fields on `1GD`/`1GT`/`2AP`).

Expiry rule: `scripts/parity/known_problems.py` re-decodes every registered VIN
against the new oracle; the **known-problems** gate fails when an `oracle-crash`
VIN no longer crashes, a `deviation` VIN no longer diverges, a deviation's
re-frozen diff shape differs from HEAD's (`refresh.deviation_shape_changes`,
refresh.py:420), or the probe could not reach the oracle (`infra-error` →
unverifiable, never a pass). A healed entry is retired, and its section is
retitled "Healed in <month>" rather than deleted (sections 1 and 4 of
KNOWN_DEVIATIONS.md). Entries never expire by age.

A second, machine-enumerated class sits beside the registry:
`scripts/stale_cache_cells.json` = `{dump, summary{stale_cells,
rows_only_in_cache, rows_only_in_recompute, cells_recompute_empty,
cache_exception_wmis}, cells: [[wmi, year, [positions]]…]}`, written by
`vpic-import --stale-cache-report` from the dump alone. It excuses a divergence
only when the diff touches only 142/143/144/156/191, the cell read is listed, and
every differing VIN position is one the cell is stale at
(uv:scripts/parity/stale_cache.py; ACCEPTANCE "second legitimate excuse").

The answer key (`scripts/parity/answerkey.py`) carries both: a VIN hash prefixed
`!` (oracle crashed, not compared) or `~` (stale-cache class proven by the
freshened-oracle counterfactual; the pinned hash is **ultravin's** and is still
compared) — uv:docs/KNOWN_DEVIATIONS.md "How the answer key carries these deviations".

## 6. Gates the refresh runs, in order

`scripts/refresh.py run MONTH` (uv:scripts/refresh.py:886-1077):

| step | what | pass condition |
|---|---|---|
| 1 | download; sha256 equal to pin → exit 0 | |
| 2 | `vpic-import --dump --month --stale-cache-report` → `vpic/` text, artifact, manifest, stale report | mechanical |
| 3 | regenerate `scripts/stale_cache_cells.json`; freeze every table ≤ 512 rows into `vpic/lookups.json` (so a renamed label is a one-line diff) | mechanical |
| 4 | `maturin develop`; cycle the docker oracle (`--lc-collate=C`) onto the new dump | mechanical |
| 5 | re-freeze `tests/parity_corpus.json` on HEAD's VIN set ∪ every registered deviation (434 VINs in 2026_10) | mechanical; crashes skipped and reported |
| 6 | **corpus** gate | every diverging corpus VIN is registered or stale-cache-excused |
| 7 | **sweep** gate: 500 generated VINs decoded live against the oracle | zero unexcused divergence; an unregistered crash fails |
| 8 | **stale-cache** gate | list consistent with its summary; not >500 newly stale cells in the charset-regression shape (recompute-only rows up >500, or stale count more than doubled and up >2,500); `cacheexceptions` empty |
| 9 | **known-problems** gate (§5) | every entry still reproduces, same shape |
| 10 | **pytest** (offline: parity corpus, normalize, generator…) | green |
| 11 | **coverage** (`scripts/coverage.py`): decode regions 100% reached, no allowance in `coverage_allowances.json` stale | green |
| 12 | **cargo test --workspace** | green |
| 13 | classify data-only vs schema-change (`git status` on `vpic/schema`, `vpic/procs`), write `target/refresh/report.{md,json}`; exit 2 on any failed gate | |

Then on the PR: `answer-key.yaml` freezes ~1.8M VIN hashes
(`tests/answerkey.json` points at `answerkey-2026_10.jsonl.zst`), and `ci.yaml`'s
`answerkey verify` re-checks ultravin against them with no Postgres on every PR
(uv:docs/DATA_REFRESH.md). Parity = field-for-field equality of the 15 columns
per element as a multiset, plus group-rank order; the one allowed
nondeterminism is intra-group row order; element 144's within-charset order is
compared as a set (uv:scripts/parity/normalize.py:38-53).

## 7. Rust-specific things and what Go does instead

| ultravin | why it is there | Go |
|---|---|---|
| rkyv zero-copy archive, `include_bytes!`, `Db::s()` reads arena strings with `from_utf8_unchecked` after a one-time validation (tables.rs:534-700) | no deserialization at load; 84 MB mapped as-is | `//go:embed` a flat little-endian layout; `encoding/binary`-free fixed-width slices over `[]byte` with `unsafe.String` for arena strings after the same one-time UTF-8 and bounds check; or decode into `[]struct` at startup (≈2.2M rows, tens of ms, simpler) — the artifact-format prototype (#17) decides |
| `Cow<'a, str>` on every `DecodingItem` field; `XXX`/source literals borrowed; losing passes never allocate | allocation discipline for 200k VIN/s | Go strings are already immutable views: slice the arena, keep `value == "XXX"` as a sentinel, resolve on the winning pass only; no `Cow` needed |
| `regex` crate for `~ keys_regex` with a hand parser fast path (matcher.rs:318-470) that falls back to the engine on anything unusual | Postgres ARE semantics; `[1-A-JT]`-class oddities | the fast path *is* the semantics for every key in the data: anchored, fixed-length, one byte per token (`.`/literal/`[class]`); port the token matcher, keep `regexp` (RE2) only for the fallback. Checked here: Go `regexp` compiles `^[1-A-JT]$` to {A,J,T,1-9,−} — the same set ultravin's `valid_chars_in_regex` yields (errors.rs:1401) — and rejects `[C-A]` as Rust and Postgres do |
| `sqlwild_to_regex` port incl. `replace('1-A','1A')` (matcher.rs:237) | the dump no longer has a `keys_regex` column; derive at build | same; keep the `1-A` patch, it is the oracle's |
| `conversion::eval` decimal-string arithmetic reproducing Postgres `numeric` mul scale and `select_div_scale` (16 sig. digits, half-away-from-zero) | `(#x# / 16.387064)::varchar` byte-identical | 6 formulas, all `#x# {*,/} const`; `math/big.Rat` + explicit scale rule, or a table of the six, each tested against the oracle's output |
| `FxHash` maps, thread-local scratch pools, `PatternScan` memo across passes, deferred default bitmask | throughput | none needed for parity; keep the stages pure functions over slices first |
| `check_digit_kernel` 17×256 weight table, `'?'` sentinel | branch-free; bug-for-bug `'?'` | a 17-iteration loop; keep `'?'` and the pos-13/14 rules of both `fVINCheckDigit` and `fVINCheckDigit2` |
| `ValidChars` type that can only render `_`-first | SQL Server collation order | a `[11]uint64` bitset per position plus one `render()` that emits `_` first — same discipline |
| clock as `now_micros` + `current_year` parameters | reproducible tests; `publicavailabilitydate <= NOW()` and `v_limit` | same: `Decode(vin, opts{Now time.Time, Year *int})`; never `time.Now()` inside the engine |
| `errorcode.weight`, `additionalerrortext` for 4/5, group ranks hardcoded (tables.rs:33-80, errors.rs:92) | constants | read them from the artifact (`errorcode` is a lookup table anyway) so a month that edits a weight is a data diff, not a code diff |

## 8. Size per stage, port order, and what each can be tested against

Line estimates are for Go without tests; ultravin's Rust line count is the
upper bound (it carries perf machinery).

| order | stage | Go est. | test against, before the next stage exists |
|---|---|---|---|
| 1 | W0-W3: sanitize, WMI, descriptor, var_keys | 60 | the three SQL functions run in Postgres on a fuzz list (short, long, non-ASCII, pos-3 `9`); or ultravin's `vin_wmi`/`vin_descriptor` via its Python wheel |
| 2 | check digit v1/v2, year raw map, position classes | 120 | `fvincheckdigit[2]`, `fvinmodelyear2` in Postgres over all 17×33 single-char mutations of a seed VIN; the `'?'` cases |
| 3 | importer: COPY parser, column mapping, timestamp → µs, sort orders, arena, header+blake3, manifest with row counts | 700 | row counts vs `vpic/manifest.json`; determinism (two builds, one hash); sorted-order invariants; per-table spot checks against `psql` |
| 4 | matcher: `sqlwild_to_regex`, LIKE branch, token matcher, per-schema index | 250 | for every distinct `pattern.keys` (≈ tens of thousands) compare `like_match`/token match against Postgres `LIKE`/`~` on a set of probe `var_keys`; `[1-A-JT]` and reversed ranges |
| 5 | core C1-C11 (pattern pass, layered sources, formula, dedup, make, conversion, specs, defaults) | 700 | call `spvindecode_core(3, rmy, vin, '***X*\|Y', true, false)` directly in Postgres and diff the item multiset (pre-resolution: value `XXX`, source, priority, keys, pattern id) — cheaper and more precise than the wrapper; conversion formulas against `select (formula)::varchar` |
| 6 | errors: `fValidCharsInKey/InRegEx`, charset recompute, E0-E6, C13-C20 | 600 | `fextractvalidcharsperwmiyear(wmi, year)` for every (wmi, year) cell → that is exactly ultravin's stale-cache report, so the first deliverable of this stage is `stale_cache_cells.json` parity with ultravin's; then `spvindecode_errorcode` on the corpus |
| 7 | wrapper W5-W9: year plan, passes, scoring, QC filter | 250 | full `spvindecode` on the frozen corpus (434 VINs with oracle rows committed in ultravin's `tests/parity_corpus.json`) and on `cover` (~164) |
| 8 | resolve + projection W10-W11, 15-column row, JSON | 200 | field-for-field vs the oracle via a Go port of `normalize.diff_rows` (multiset by element, group-rank order, 144 as a set) |
| 9 | harness: oracle client, freeze, sweep, normalize, known-problems probe, stale-cache gate, answer-key build/verify | 900 (Python today, 2.8k lines in `scripts/parity` + 1.1k `refresh.py`) | reuse ultravin's `tests/parity_corpus.json` and answer-key asset as the first corpus; the ~1.8M-VIN key is the real gate |
| 10 | generator + cover (optional; ultravin ships them as library API) | 600 | sweep gate needs *a* generator; the Python `scripts/parity/generator.py` (299 lines, Postgres-backed) is the smaller model |

Total ≈ 3.5k lines of Go engine + importer, ≈ 1k harness. Stage 5 is the one
with real risk: the orderings in §3 are where a port silently diverges, and the
corpus only catches the ones it contains — the `cover` set and the 1.8M key are
what catch the rest.

Decisions this unblocks and the facts they need: artifact format (#17: §2 schema
and sort orders, §7 row 1), API contract (#18: §0 output row, §1 W11, §7 clock
row), parity gate (#19: §6 table, normalize rules), deviation registry (#22: §5
shape, expiry, the `WMIYearValidChars` choice in §4).

# NHTSA's dump: where it is, how it is named, how a new month is detected, what `pg_restore` needs, and the terms of use

Resolves #13. Investigated 2026-11-07 against vpic.nhtsa.dot.gov, the
2026_10 dump itself (downloaded, restored into Postgres 17), the PostgreSQL
source, and ultravin at `dbc9777` (2026-11-03). Terms from GLOSSARY.md:
dump, data month, refresh, oracle. Anything not observed is marked
[INFERENCE].

## 1. Where it is

| Item | Value | Source |
| --- | --- | --- |
| Listing page | `https://vpic.nhtsa.dot.gov/downloads/` (one HTML page; always shows only the newest month) | page read 2026-11-07 |
| Postgres custom | `https://vpic.nhtsa.dot.gov/downloads/vPICList_lite_<YYYY_MM>.custom.zip` | listing page |
| Postgres plain | `https://vpic.nhtsa.dot.gov/downloads/vPICList_lite_<YYYY_MM>.plain.zip` | listing page |
| SQL Server | `https://vpic.nhtsa.dot.gov/downloads/vPICList_lite_<YYYY_MM>.bak.zip` | listing page |
| Path case | `/downloads/` and `/Downloads/` both answer 200 with identical bytes (ultravin uses the capitalised form) | HEAD both; `ultravin@dbc9777:scripts/refresh.py:69` |
| FAQ | `https://vpic.nhtsa.dot.gov/Downloads/FAQ` (restore steps, `spVinDecode` parameters) | page read 2026-11-07 |
| Release notes | `https://vpic.nhtsa.dot.gov/Downloads/ReleaseNotes` (one entry per month) | page read 2026-11-07 |
| Email list | `https://vpic.nhtsa.dot.gov/mfrportal/EmailSubscription/Manage` ("sends the persons that subscribe the release notes as they come out") | `https://vpic.nhtsa.dot.gov/api/home/index/faq` |
| Help desk | `manufacturerinfo@dot.gov` | downloads page footer; API FAQ |

There is no `vPICList_<month>` ("full") file, no `latest` alias, no directory
index, no RSS/feed, and no checksum file: `vPICList_2026_10.custom.zip`,
`vPICList_full_2026_10.bak.zip`, `vPICList_lite.custom.zip`,
`vPICList_lite_latest.custom.zip`, `vPICList_lite_2026_10.zip`,
`*.sha256`, `*.md5`, `checksums.txt` all return 404 (`SHA256SUMS` 302s to
`/Error/NotFound`). "lite" is the only published tier; the downloads page
says it "is limited to VIN Decoding functionality only — to retrieve other
information (such as makes, models, variables, attributes, etc.), you will
still need to use vPIC APIs."

### What is listed, 2026_10

| File | Page says | `Content-Length` | `Last-Modified` | Archive member |
| --- | --- | ---: | --- | --- |
| `vPICList_lite_2026_10.bak.zip` | "MS SQL Server 2019 and newer", 179 MB, updated 10/17/2026 | 188,047,704 | Thu, 15 Oct 2026 15:08:21 GMT | not downloaded |
| `vPICList_lite_2026_10.custom.zip` | "PostgreSQL version 17 and newer", 68.0 MB, updated 10/17/2026 | 71,394,797 | Wed, 14 Oct 2026 14:40:00 GMT | `vPICList_lite_2026_10.backup`, 77,072,408 B |
| `vPICList_lite_2026_10.plain.zip` | same, 73.1 MB | 76,705,492 | Wed, 14 Oct 2026 14:39:42 GMT | `vPICList_lite_2026_10.sql`, 337,183,947 B |

SHA-256 measured 2026-11-07: custom zip
`373e6a8701f60a38e9fab9079d83c8947a75753e76720c116d8ba329ee03d2c1`, its
`.backup` `f4f19cdae264b436ed275597dbb74476fcb8486091216df8a61c092471f8b3c6`;
plain zip `863445dfd77807c1acee59d5991d54b0c8add802f9402dfa696d540f548c2282`,
which equals ultravin's pin (`ultravin@dbc9777:vpic/manifest.json:5`).
Download of the custom zip from this workstation: 2.7 s at ~26 MB/s. The
server sends `Accept-Ranges: bytes` and honours `Range` (206), so resumable
and partial fetches work.

## 2. How it is named and what months exist

`YYYY_MM` is the month the file is published in, not a data cut-off date
(the 2026_10 files were written 14 Oct 2026 and the release notes entry is
17 Oct 2026). HEAD on all three variants for 2025_01..2026_12, 2026-11-07:

| Month | custom (bytes, Last-Modified) | plain | bak | Release-notes date |
| --- | --- | --- | --- | --- |
| ≤ 2025_11 | 404 | 404 | 404 | (older `.bak` link lived elsewhere; v3.67 "moved" it) |
| 2025_12 | 69,948,724 · Tue 09 Dec | 74,433,079 · Tue 09 Dec | 180,838,752 · Tue 09 Dec | 12/12/2025 (v3.67: first PostgreSQL 17 file) |
| 2026_01 | 69,890,957 · Wed 14 Jan | 74,931,562 · Wed 14 Jan | 182,278,177 · Wed 14 Jan | 1/17/2026 |
| 2026_02 | 70,222,792 · **Tue 03 Mar** | 75,281,229 · **Tue 03 Mar** | 182,399,957 · Wed 18 Feb | 2/21/2026 |
| 2026_03 | 70,413,098 · Thu 12 Mar | 75,463,516 · Thu 12 Mar | 182,851,699 · Thu 12 Mar | 3/14/2026 |
| 2026_04 | 70,579,871 · Wed 15 Apr | 75,681,099 · Wed 15 Apr | 183,765,453 · Tue 14 Apr | 4/18/2026 |
| 2026_05 | 70,800,241 · Fri 15 May | 75,909,448 · Fri 15 May | 184,624,555 · Wed 13 May | 5/16/2026 |
| 2026_06 | 70,829,622 · Thu 11 Jun | 75,896,519 · Thu 11 Jun | 193,513,243 · Thu 11 Jun | 6/13/2026 |
| 2026_07 | 70,906,059 · Wed 15 Jul | 75,991,860 · Wed 15 Jul | 186,253,428 · Thu 16 Jul | 7/18/2026 |
| 2026_08 | 70,886,151 · Thu 13 Aug | 75,917,209 · Thu 13 Aug | 195,223,584 · Thu 13 Aug | 8/15/2026 |
| 2026_09 | 71,113,971 · Wed 16 Sep | 76,201,016 · Wed 16 Sep | 187,417,100 · Wed 16 Sep | 9/19/2026 |
| 2026_10 | 71,394,797 · Wed 14 Oct | 76,705,492 · Wed 14 Oct | 188,047,704 · Thu 15 Oct | 10/17/2026 |
| 2026_11, 2026_12 | 404 | 404 | 404 | — |

Sources: HEAD responses (`Content-Length`, `Last-Modified`, `ETag`);
`https://vpic.nhtsa.dot.gov/Downloads/ReleaseNotes`;
`https://vpic.nhtsa.dot.gov/api/Home/Index/ReleaseNotes` (monthly
"standalone vPIC database has been updated" entries back through 2025 at
least: 5/17, 6/14, 7/19, 8/16, 9/13, 10/18, 11/15/2025).

Cadence, from the table:

- One dump a month, every month since the Postgres files began (2025_12).
  The API release notes show the same monthly rhythm for the `.bak` file
  through 2025.
- Files land on a weekday between the **9th and 16th** (13:41–20:18 UTC);
  the release-notes entry (and the "updated on" date on the page) is the
  following **Saturday, 12th–21st**. ultravin's comment "day 9-18,
  13:15–19:16 UTC" (`ultravin@dbc9777:.github/workflows/data-refresh.yaml:28`)
  agrees with the file dates.
- Published files get re-touched: 2026_02's custom and plain files carry a
  3 Mar 2026 `Last-Modified`, two weeks after the bak and the release note.
  ultravin notes the same ("occasionally re-touches a published file",
  `refresh.py:10`) and detects it by `Content-Length` against the pinned
  `dump_bytes` (`refresh.py:230-247`). Whether a re-touch changes content
  is not observable after the fact [INFERENCE: treat size *and* SHA-256 as
  the identity of a data month, never the name alone].
- Retention: 2025_11 and earlier are gone; 11 months are online now.
  ultravin says "~8 months" (`refresh.py:9`); the observed window is at
  least 11. [INFERENCE: do not rely on any month staying downloadable;
  archive the bytes of every month this project ships.]

## 3. Detecting a new month

| Method | Works? | Notes |
| --- | --- | --- |
| HEAD `…/vPICList_lite_<next month>.custom.zip` | **yes** | 404 until published, then 200 with `Content-Length`, `Last-Modified`, `ETag`. This is what ultravin does: `month_candidates()` walks every month after the pin up to the current UTC month, newest first, and HEADs each (`refresh.py:192-247`); a `Content-Length` differing from the pinned `dump_bytes` on the pinned month is a "reissue". Daily cron 20:23 UTC (`data-refresh.yaml:28`). |
| Scrape `/downloads/` for `vPICList_lite_(\d{4}_\d{2})` | yes | Page shows exactly one month; same answer as HEAD with HTML parsing on top. Useful as a cross-check only. |
| `Last-Modified`/`ETag` of `/downloads/` HTML | no | The page sends neither header. |
| Checksums from NHTSA | no | None published (§1). Record own SHA-256 and byte size per month. |
| Email subscription | human only | Release notes mailed at publication; no machine feed. |

Recommended for the refresh: ultravin's probe verbatim (HEAD the next
month(s), then size-check the pinned month), run daily after ~20:00 UTC,
plus an "N months behind" warning (ultravin warns at 2,
`refresh.py:259-264`). Because `Last-Modified` moves on re-touch without
the name changing, the refresh must key on `(month, Content-Length,
SHA-256)`, not on the name.

## 4. What `pg_restore` needs

Measured on the 2026_10 custom dump with `pg_restore (PostgreSQL) 18.6`
(Homebrew `libpq`) and a `postgres:17` container (17.11, aarch64).

| Property | Value | Source |
| --- | --- | --- |
| Format | `PGDMP` custom, `Dump Version: 1.16-0`, compression gzip, 782 TOC entries | `pg_restore -l` |
| Produced by | `pg_dump 18.3` from a `17.6` server, 2026-10-14 10:37 EDT, dbname `vPICList_lite_2026_10` | `pg_restore -l` header |
| Minimum `pg_restore` | **17.x** — archive version 1.16 was introduced in PG 17 (`K_VERS_1_16`, `postgres@REL_17_STABLE:src/bin/pg_dump/pg_backup_archiver.h`; REL_16 tops out at 1.15). `postgres:16`'s `pg_restore -l` fails: `unsupported version (1.16) in file header`. | PostgreSQL source; tested |
| Minimum server | NHTSA: "PostgreSQL version 17 and newer". The preamble sets `transaction_timeout` (new in 17); on 16 that is one ignorable `ERROR: unrecognized configuration parameter` for the plain file (tested, `psql 16.15`), but the custom archive cannot be read by 16's `pg_restore` at all. | downloads page; tested |
| Plain-file `psql` minimum | the file opens with `\restrict <token>` (pg_dump 18.3 output); `psql` 16.15 accepted it. [INFERENCE: `\restrict` landed in the Aug 2025 minors (17.6/16.10/15.14/14.19/13.22), so older `psql` minors reject the file.] | plain file line 5; tested |
| Extensions | none — only `plpgsql` is present after restore (`select extname from pg_extension`). No `CREATE EXTENSION` in the plain file. | tested |
| Flags | `--no-owner --no-privileges` as NHTSA documents (objects are owned by `postgres`/`pg_database_owner` in the TOC). Target must be an existing database **without** a `vpic` schema: restoring the custom file a second time into the same DB exits 1 with "errors ignored on restore: 502" (406 `already exists`); `--clean --if-exists` over an existing load exits 0 (13 s). | downloads page; tested |
| Restore time | 20.7 s (fresh DB, 2026_10, Apple Silicon container) | tested |
| Restored size | `pg_database_size('vpic')` = 1,399,445,171 B (1335 MB) | tested |
| Rows | 10,996,849 live tuples across 97 tables (equals ultravin's `manifest.json` `total_rows`) | tested; `ultravin@dbc9777:vpic/manifest.json:10` |

Largest relations after restore (`pg_total_relation_size`):

| Table | Size | Rows |
| --- | ---: | ---: |
| `wmiyearvalidchars` | 953 MB | 8,806,153 |
| `pattern` | 309 MB | 1,682,444 |
| `vehiclespecpattern` | 27 MB | 231,867 |
| `wmi_vinschema` | 6.5 MB | 41,810 |
| `make_model` | 4.3 MB | 32,070 |
| `vinschema` | 3.1 MB | 25,299 |
| `manufacturer` | 3.1 MB | 23,098 |
| `wmi` | 3.1 MB | 13,014 |

So the restored oracle is ~1.3 GB, two-thirds of it the
`wmiyearvalidchars` cache; an artifact that recomputes that cache from
`pattern` rows does not need to carry it (ultravin's `stalecache.rs` diffs
the cache against the recompute: `ultravin@dbc9777:crates/ultravin-build/src/stalecache.rs`).

## 5. What the dump carries

TOC composition (`pg_restore -l`): 1 schema (`vpic`), 1 type, 15 functions,
97 tables (+97 TABLE DATA), 3 views, 273 sequence entries (91 sequences ×
SEQUENCE/SET/OWNED BY), 157 constraints, 15 FKs, 27 indexes, 91 defaults.

**`spVinDecode` and every helper ship in the dump — nothing has to be
vendored to run the oracle.** The 15 functions, all in schema `vpic`,
language plpgsql, 0 extensions:

| Function | Signature |
| --- | --- |
| `spvindecode` | `(character varying, boolean, integer, boolean, boolean)` → `TABLE(groupname, variable, value, itempatternid, itemvinschemaid, itemkeys, itemelementid, itemattributeid, itemcreatedon, itemwmiid, code, datatype, decode, itemsource, itemtobeqced)` |
| `spvindecode_core` | `(integer, integer, varchar, varchar, boolean ×5)` |
| `spvindecode_errorcode` | `(varchar, integer)` |
| `spvindecodemultiple` | `(varchar[])` — added 2026_09 (v4.09), max 100 VINs |
| `felementattributevalue` | `(integer, varchar)` |
| `ferrorvalue` | `(varchar)` |
| `fextractvalidcharsperwmiyear` | `(varchar, smallint)` |
| `fvalidcharsinkey`, `fvalidcharsinregex` | `(varchar)` |
| `fvincheckdigit` | `(varchar)`; `fvincheckdigit2` `(varchar, boolean)` |
| `fvindescriptor`, `fvinmodelyear2`, `fvinwmi` | `(varchar)` |
| `sqlwild_to_regex` | `(text)` |

Parameters per the FAQ: `V varchar(17)` required; `IncludePrivate`
(NCSA variables), `Year` (model year, else computed from the VIN),
`IncludeAll` (emit empty variables), `NoOutput` (write to table
`decodingoutput` instead). Verified: `select * from
vpic.spvindecode('5UXWX7C5XBA123456')` returns 37 rows (Make BMW, Model X3,
Model Year 2011, Error Code 1 for the made-up check digit);
`spvindecodemultiple(array[...])` works. Views: `vncsabodytype`,
`vncsamake`, `vncsamodel`. Type: `vpic.tbldecodingitem`. Table
`decodingoutput` is shipped empty.

Why ultravin still commits `vpic/procs/*.sql`: not because the dump lacks
them. Its importer `vpic-import` splits the *plain* dump into one file per
object and strips the volatile `pg_dump` TOC/OID comments "so upstream
changes stay diffable" (`ultravin@dbc9777:crates/ultravin-build/src/main.rs:1-8`);
a CI gate requires the importer to reproduce the committed text
byte-identically, and the refresh classifies a month as `schema-change` when
`git status` moves anything under `vpic/schema` or `vpic/procs`
(`docs/DATA_REFRESH.md:90-92`). The oracle itself loads NHTSA's dump stream
unmodified, never the committed copies (`scripts/oracle.sh:3-5,36-38`).
Its NOTICE states the same: "nothing was ported, translated, or rewritten"
(`ultravin@dbc9777:NOTICE`). Proc text has changed twice in ultravin's
history: 2026_09 (`spvindecode_errorcode.sql` rewritten, `spvindecodemultiple.sql`
added; commit `6f3e4ff`) and 2026_07 (commit `26513bd`).

Variant differences: the SQL Server `.bak` is the original (restore name
`vPICList_Lite`, `EXEC dbo.spVinDecode`, `tblVinList` TVP); NHTSA "fixed
data inconsistencies between SQL Server and PostgreSQL formats for VIN
Decode results" in v4.05 (5/16/2026) and added `Parent Manufacturer ID` in
v4.03 — evidence that the two formats are produced separately and can
disagree. The Postgres plain and custom files are the same `pg_dump` run
(same minute, same `Dumped from 17.6 / by 18.3`); choose custom for
`pg_restore -l` introspection and the smaller download (71 MB vs 77 MB), plain for text diffing (ultravin's
choice).

## 6. Terms of use

vPIC publishes no licence text of its own on the downloads page or API
page. The applicable statements, verbatim:

**NHTSA Terms of Use**, `https://www.nhtsa.gov/about-nhtsa/terms-use`
(nhtsa.gov answers 403 to non-browser clients; read from the Wayback capture
`https://web.archive.org/web/20261019203729/https://www.nhtsa.gov/about-nhtsa/terms-use`;
it is the "Terms of Use" link on `https://www.nhtsa.gov/about-nhtsa/web-policies-notices`,
which vpic.nhtsa.dot.gov's footer links as "Web Policies & Notices"):

> **OWNERSHIP** — Information presented on this web site is considered
> public information and may be distributed or copied. However, all
> information submitted to NHTSA/DOT via this site shall be deemed and
> remain the property of NHTSA/DOT, except those submissions made under
> separate legal contract. NHTSA/DOT shall be free to use, for any purpose,
> any ideas, concepts, or techniques contained in information provided to
> NHTSA/DOT through this site.

> **RESTRICTION OF LIABILITY** — The NHTSA/DOT makes no claims, promises or
> guarantees about the accuracy, completeness, or adequacy of the contents
> of this web site and expressly disclaims liability for errors and
> omissions in the contents of this web site. No warranty of any kind,
> implied, expressed or statutory, including but not limited to the
> warranties of non-infringement of third party rights, title,
> merchantability, fitness for a particular purpose and freedom from
> computer virus, is given with respect to the contents of this web site or
> its links to other Internet resources. Reference in this web site to any
> specific commercial products, processes, or services, or the use of any
> trade, firm or corporation name is for the information and convenience of
> the public, and does not constitute endorsement, recommendation, or
> favoring by NHTSA/DOT.

**vPIC API FAQ**, `https://vpic.nhtsa.dot.gov/api/home/index/faq`:

> Is there a licensing requirement or registration requirement to use the
> APIs? No, NHTSA is a government agency and the services provided on the
> API are free for use by the public as an offering as a part of our Open
> Data initiatives; and There is no need to register your application to
> use the services.

> […] this is a public resource so if you are planning on large batch
> processes we ask that you time them either at night (based on EST, or on
> the weekend).

**Downloads page**, `https://vpic.nhtsa.dot.gov/downloads/`: "Standalone
vPIC Databases are intended for use by developers and researchers who
prefer to use a local database instead of calling the vPIC API. There are
quotas and rate limits associated with the usage of the vPIC API, and you
can avoid reaching the limits by using the standalone databases."

Reading: the dump is a U.S. Government work (17 U.S.C. § 105 — the basis
ultravin's NOTICE gives, `ultravin@dbc9777:NOTICE`), NHTSA says it "may be
distributed or copied", imposes no registration or licence, and disclaims
all warranty. This repo's NOTICE already describes the data as "a work of
the United States Government" (`nhtsa-vin-decoder@5a107a2:NOTICE`), which
these pages support. [INFERENCE: nothing on nhtsa.gov addresses
redistribution of a *derived* artifact explicitly; "may be distributed or
copied" plus § 105 is the whole basis, and the NOTICE should quote the
Terms of Use URL and the "no endorsement" sentence.]

## 7. What this unblocks

- **Postgres oracle (#19/#20 per the map):** `postgres:17` image or newer,
  `pg_restore --no-owner --no-privileges` into an empty DB (`--clean
  --if-exists` for reloads), ~21 s and 1.3 GB per month, no extensions, and
  `vpic.spvindecode(vin, include_private, year, include_all, no_output)` is
  in the dump. No SQL has to be vendored for correctness; vendoring the
  proc text (ultravin's way, via the plain file) is for *diffing* upstream
  changes, which the parity gate wants because the procedure does change
  (2026_07, 2026_09).
- **Refresh detection:** HEAD the next month's `.custom.zip` daily after
  20:00 UTC; identity is `(month, Content-Length, SHA-256)`; warn at two
  months behind; keep a copy of each month's bytes because NHTSA drops old
  months and re-touches current ones.
- **Parity-gate design:** `spvindecodemultiple` (≤100 VINs) exists for
  batch oracle runs since 2026_09; the SQL Server and Postgres outputs are
  not guaranteed identical, so the oracle is the Postgres function and
  nothing else.

## Verified vs bounded

| Verified by running | Not done |
| --- | --- |
| HEAD on 24 months × 3 variants; full download, unzip, `pg_restore -l`, restore on 17.11, size/row/function queries, decode calls, second-restore and `--clean` behaviour; PG16 `pg_restore` refusal; PG16 `psql` on the plain preamble | The 179 MB `.bak` (not downloaded); the content of a re-touched month versus its first publication (unobservable now); live nhtsa.gov terms page (403 to curl; Wayback capture of 2026-10-19 used) |

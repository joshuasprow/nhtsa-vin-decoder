# Part-type taxonomies that could be a spine

Ticket: [#119](https://github.com/joshuasprow/nitrovin/issues/119), on the map
[#117](https://github.com/joshuasprow/nitrovin/issues/117). Date: 2026-11-10.
Feeds the grilling ticket [#121](https://github.com/joshuasprow/nitrovin/issues/121)
(the part-type spine when nitrovin ships none). eBay's category tree is
[#118](https://github.com/joshuasprow/nitrovin/issues/118)'s subject and is not
researched here.

**Answer: no taxonomy is both shippable and at a salvage yard's grain.** The three
whose grain fits part-type translation (Auto Care PCdb, Hollander part types,
TecDoc generic articles) are licensed, and PCdb's and TecDoc's terms reach the
ids themselves. The ones nitrovin could ship (NHTSA's recall components, the
Harmonized System, Wikidata) are too coarse, built for safety, customs or
encyclopedic use, or unevenly curated. So nitrovin cannot carry a part-type
spine the way it carries vPIC: the spine has to be one the user supplies or
names (§4). TecDoc keys generic articles to vehicle classes and engine types,
and Hollander's coverage of a part type starts at a model year, so part types
do depend on the vehicle in practice (§3).

## 1. Comparison

"Ship" means nitrovin commits the taxonomy (ids and names) to its public repo
or binary. "Reference" means nitrovin documents or validates the id space
without carrying its contents. "Accept" means the taxonomy arrives only inside a
user's vocabulary file, and nitrovin treats its ids as opaque.

| Taxonomy | Entries | Grain | Id stability | Licence | What nitrovin could do |
| --- | --- | --- | --- | --- | --- |
| **Auto Care PCdb** (ACES/PIES part terminologies) | "40+ thousand part terminologies" (2026); "over 36,000 product descriptions" (2022) | Aftermarket replacement unit, many vehicle-independent: "Disc Brake Pad Set" (1684), "Cabin Air Filter" (6832), also sponges, candy bars, T-shirts. Category → Subcategory → Part Terminology; positions (Front, Front Left, …) sit beside the terminology, not inside it | Integer ids; a `PartsSupersession` table maps retired `OldPartTerminologyID` → `NewPartTerminologyID`; published monthly (daily via API) | Paid annual subscription. The licence covers "the associated database IDs, the taxonomy associated with the database IDs"; use is "Internal Business Use" only; distribution only to other subscribers | **Accept only.** Can't ship or publish a list of ids. A user who subscribes can map their entries to PCdb ids in a private vocabulary, but that file is a licensed "Derivative Work" and can't become a public vocabulary |
| **Hollander part types** (Interchange) | US: "more than 160 active part types" with interchange (2014). UK/EU Hollander: "662+" part types | Recycler unit, a dismantled assembly: engine, transmission, bumper, fender, door, catalytic converter (PT 335), torque converter (PT 407). The 3-digit type is the first segment of an interchange number (`300-55643A`) | 3-digit codes held for decades. New types get new codes, and parts already stocked under the old type must be re-inventoried by hand (PT 371/373 split off PT 197/323, 2017) | Proprietary, sold by Hollander (Solera). No public list or terms. The map already records that it can't ship | **Accept only** (e.g. Snyder's own data, never committed) |
| **TecDoc generic articles** (TecAlliance) | Not published on the public pages read (gap). Key is 5 digits (`GenArtNo`, max 99,999) | Aftermarket replacement unit; each generic article links to a standardised name, an assembly group and a usage (tables 320/323/324/325) | Numeric key; a `Delete` flag marks generic articles "earmarked for deletion"; new ones are added on request | Licensed reference data. Even the format spec says that transferring the document "in any form, also in part, requires the written approval of TecAlliance GmbH" | **Accept only** |
| **NHTSA recall components** (`COMPNAME`) | 667 distinct strings, 42 top-level systems, up to 5 levels deep (measured, all recalls since 1966) | Safety system/defect: `SERVICE BRAKES, HYDRAULIC:FOUNDATION COMPONENTS:DISC:CALIPER`, `STRUCTURE:BODY:BUMPERS`. Covers no fenders, and doors only as latches and locks | **No ids:** the name is the key. Renames leave both forms in history: `ELECTRONIC STABILITY CONTROL` (1998–2005) became `…(ESC)` (2008–2026). In May 2025 "All Descriptions have been updated" | US government work, public domain (17 U.S.C. §105), like vPIC | **Could ship**, but as a spine it's too coarse and has no ids |
| **Harmonized System / HTSUS** heading 8708 ("parts … of motor vehicles") | 15 six-digit subheadings in 8708; 156 HTSUS rows at every level | Customs category: bumpers, brakes, gear boxes, drive axles, road wheels, suspension, radiators, exhaust, clutches, steering, airbags, other. Engines, lamps, glass, seats and tyres are in other chapters | 6-digit codes revised by edition (HS 2022; HS 2028 from 1 Jan 2028); codes split and merge between editions | HTSUS is a US government publication. The WCO holds the HS nomenclature itself (redistribution of WCO texts not checked) | Could ship HTSUS codes. Far too coarse ("8708.99 Other") |
| **Google product taxonomy** | 32 entries under `Motor Vehicle Parts` (id 899); 5,596 lines in the whole file | Retail shelf: Braking, Engine Parts, Exhaust, Lighting, Mirrors, … | Numeric ids. The file has been frozen at version `2021-09-21` | The file states no licence; Google publishes it for Merchant Center feeds | Reference only. Too coarse |
| **GS1 GPC**, segment 77 "Automotive" | June 2016 file: 2 families, 43 classes, 295 bricks | Retail accessory and maintenance: "Bumpers", "Headlights (Automotive)", "Starter", "Transmission Fluid", "Pin Striping" | 8-digit brick codes; deltas published per release | Free download from gs1.org; redistribution terms not found on the pages read (gap) | Reference only. Retail grain, not salvage |
| **Wikidata** (subclasses of [Q6405028](https://www.wikidata.org/wiki/Q6405028) "automotive part") | 60 direct subclasses, 125 transitive (measured) | Mixed: "fender", "car door", "catalytic converter" next to Google-taxonomy imports like "motor vehicle engine part" | Q-ids are permanent, and a merge leaves a redirect. But anyone can edit or reclassify the hierarchy | CC0 | Could ship. Thin, uneven, unreviewed |
| eBay parts categories | see [#118](https://github.com/joshuasprow/nitrovin/issues/118) | — | — | — | — |

### Grain distance

On a straight count, PCdb is two orders of magnitude finer than Hollander:
40,000+ terminologies against ~160 active (US, 2014) or 662+ (UK) Hollander
types, so 60–250 PCdb terms per Hollander type on average. The real relation
isn't a ratio. [INFERENCE from the published examples, no crosswalk is public]
The two count different things. Hollander counts what a recycler pulls off a car
(an engine, a door, a front-end assembly). PCdb counts what a parts store
replaces or sells, including components, gaskets, fluids and non-vehicle goods.
So a Hollander type maps to many PCdb terms (an engine to its blocks and
assemblies), to one, or to none (a door shell or front-end assembly may have no
single PCdb term). Thousands of PCdb terms (filters, chemicals, candy bars) have
no Hollander counterpart at all. TecDoc sits at PCdb's grain. NHTSA, HS, Google
and GPC are coarser than Hollander: one HS subheading or Google category
covers dozens of Hollander types. Measuring how far apart the grains really are
needs PCdb and Hollander side by side, and both are licensed (gap, §5).

## 2. Sources and evidence

### Auto Care PCdb

| Fact | Source |
| --- | --- |
| "a product classification hierarchy, which standardizes product terminologies in a coded manner"; "40+ thousand part terminologies"; covers "both vehicle and non-vehicle specific" products (brake pads … candy bars, T-shirts). | [autocare.org PCdb page](https://www.autocare.org/data-and-information/data-standards/databases/product-classification-database-pcdb) |
| Published monthly (JSON, Access, SQL Server) and daily (JSON API). Requires "an annual paid subscription" to a VCdb package or the PAdb. | same page |
| 2022: "Containing over 36,000 product descriptions"; "Includes application specific and non-application specific product descriptions"; example rows: `1684 Disc Brake Pad Set` × positions 22 Front, 103 Front Left, …; an ACES `<App>` carries `<BaseVehicle id>` (VCdb), `<PartType id="6832"/>` (PCdb, Cabin Air Filter), `<Position id>` (PCdb). | [Auto Care ACPN 2022 bootcamp deck](https://www.autocare.org/docs/default-source/events/event-materials/acpn/01---acpn22-bootcamp-final.pdf) |
| Subscription prices: PAdb (which includes PCdb) runs from $1,050 (member, under $1M revenue) to $10,868 (non-member, over $10B revenue) a year; entry package $2,500. | [autocare.org subscriptions](https://www.autocare.org/data-standards/subscriptions) |
| Licensed Property "includes the associated database IDs, the taxonomy associated with the database IDs, all language translations, and the aggregate data". The grant is "Internal Business Use by Authorized Users only … and not for copying or otherwise making available the Auto Care Licensed Property or the Derivative Works … to Third Parties". Distribution is allowed only to Authorized Users who are "themselves an independent Subscriber". "Derivative Works" covers anything "derived from the Auto Care Licensed Property". The licence also names "ACES or PIES table identification numbers" as a Proprietary Notice. | [Auto Care Technology Product License Agreement, 2025-02-28](https://www.autocare.org/docs/default-source/data-standards/technology-license-agreement_02282025.pdf) (pages 2–7 are images; read by OCR) |
| Table shapes: `Parts(PartTerminologyID, PartTerminologyName, PartsDescriptionID, RevDate)`, `Categories`, `Subcategories`, `Positions`, `CodeMaster(CodeMasterID, CategoryID, SubCategoryID, PartTerminologyID, PositionID, RevDate)` unique on the four ids, `PartsSupersession(OldPartTerminologyID, …, NewPartTerminologyID, …, RevDate)`, `PartsToAlias`, `PartsToUse`, `PartsRelationship`. | Schema only, no data: [curt-labs/CurtSchema `pcdb.sql`](https://github.com/curt-labs/CurtSchema/blob/master/pcdb.sql); table list in [TranstarIndustries/autocare-python `pcdb.py`](https://github.com/TranstarIndustries/autocare-python/blob/main/autocare/databases/pcdb.py) |

### Hollander

| Fact | Source |
| --- | --- |
| A lookup needs "Part type (selected from an arbitrary list released by Hollander)", model and year. An interchange number is "A 3-digit part type number", "A 5-digit number", and optionally a letter (`300-55643A`, `105-04671F`). | [ISoft wiki, Hollander Interchange](https://wikido.isoftdata.com/index.php/Hollander_Interchange) (a yard-software vendor) |
| "coverage for more than 160 active part types across more than 1,000 models" (Interchange 80.04, 2014). | [Recycling Today, 2014-12-23](https://www.recyclingtoday.com/news/hollander-interchange-update), quoting Hollander |
| Hollander International (UK/EU): "Part Types 662+", "Makes 339+", "Models 2,263+", "IC Lines 2,052,618". | [hollanderinternational.com](https://www.hollanderinternational.com/part-interchange-services) |
| Interchange 83.01 (2017) added PT 371 Exhaust Fluid Pump and PT 373 Exhaust Fluid Tank: "If these parts were previously inventoried incorrectly under Fuel Tank (PT 197) and/or Fuel Pump (PT 323), then these parts will need to be manually re-inventoried". The same release split VW Golf GTI from Golf ("Model Splits"). | [Hollander Interchange 83.01 feature summary](https://www.training.hollandersolutions.com/powerstation/downloads/interchange/8301/Interchange83.01.pdf) |
| Interchange 92.01 (2026): "PT335 Catalytic converter. Coverage begins from MY2022"; "PT407 Torque converter. Coverage begins from MY2022"; separate "Reference Part Types". | [Hollander Interchange 92.01 feature summary](https://updates.hollandersolutions.com/Interchange/9201/Feature%20Summary%20v92.01%20Interchange.pdf) |

### TecDoc

| Fact | Source |
| --- | --- |
| Table 320 Generic Articles: `GenArtNo` (5 N, "Unambiguous number of the generic article"), `NormTermNo` (→323 standardised article description), `AssGrpNo` (→324 assembly group), `UsageNo` (→325), flags `OK_PC`, `OK-CV`, `OK-Eng`, `OK-Axle` ("0: not allowed in … linkage"), `OK-Universal`, `OK-Ind` (vehicle-independent search), `Delete` ("1: GenArt is earmarked for deletion"). "All generic articles with the flag OK-PC are allowed for a linkage to LCV." | [TecDoc Data Format 2.7, rev 2.0.33 (2026-10-05)](https://a.storyblok.com/f/297549/x/9ae209aefb/tecdoc-data-format_version_2-7_en_2-0-33.pdf), pp. 121–122 |
| Table 340 "DQM – Generic Articles and Engine Types Permission": "Permitted allocations of Engine Types to Generic Articles", added to avoid "allocation of exhaust related products to an electric vehicle". Any combination is allowed if a generic article is absent. | same, p. 135 |
| "Copyright © TecAlliance GmbH – The transfer or transmission of this document in any form, also in part, requires the written approval of TecAlliance GmbH." | same, title page |
| Catalogue scale: "9,800,000+ Article Data", "450,000,000+ Vehicle Linkages", "190,000+ TecDoc Vehicle Types". | [TecAlliance shop](https://shop.tecalliance.net/tecdoc) |
| A new generic article is added on request (SEG's 48V BRM, 2025). | [SEG Automotive news, 2025-12-15](https://www.seg-automotive.com/news/48v-boost-recuperation-machine-brm-from-seg-automotive-now-available-as-a-new-generic-article-ga-in-tecdoc) |

### Open and government alternatives

| Fact | Source |
| --- | --- |
| Recall file field 7 `COMPNAME CHAR(256) Component Description`; field 24 `RCL_CMPT_ID` identifies a recalled component instance, not a component type. "All Descriptions have been updated" (May 2025). | [NHTSA `RCL.txt`](https://static.nhtsa.gov/odi/ffdd/rcl/RCL.txt) |
| 329,391 recall rows; 667 distinct `COMPNAME`; 42 top-level systems; depth 1–5 = 38/170/282/137/40 names; 644 names on vehicle (`V`) recalls; 542 seen on reports received 2022 or later. Spot check: no `FENDER`; `DOOR` only under `LATCHES/LOCKS/LINKAGES`. | measured 2026-11-10 on [`FLAT_RCL_POST_2010.zip`](https://static.nhtsa.gov/odi/ffdd/rcl/FLAT_RCL_POST_2010.zip) + [`FLAT_RCL_PRE_2010.zip`](https://static.nhtsa.gov/odi/ffdd/rcl/FLAT_RCL_PRE_2010.zip) |
| US government works have no copyright. | [17 U.S.C. §105](https://www.law.cornell.edu/uscode/text/17/105) |
| HTSUS heading 8708: 156 rows, 15 six-digit subheadings (8708.10 bumpers … 8708.99 other). | measured via [USITC HTS REST search](https://hts.usitc.gov/reststop/search?keyword=8708) |
| HS 2028, "the eighth edition", enters into force 1 January 2028. | [WCO HS 2028 amendments](https://www.wcoomd.org/en/topics/nomenclature/instrument-and-tools/hs-nomenclature-2028-edition/amendments-effective-from-1-january-2028.aspx) |
| Google taxonomy header `# Google_Product_Taxonomy_Version: 2021-09-21`; 5,596 lines; 32 under `899 - … > Motor Vehicle Parts`. | measured on [taxonomy-with-ids.en-US.txt](https://www.google.com/basepages/producttype/taxonomy-with-ids.en-US.txt); usage: [Merchant Center help](https://support.google.com/merchants/answer/6324436) |
| GPC Automotive segment `77000000`: families `77010000` Automotive Accessories and Maintenance, `77030000` Cars and Motorcycles; 43 classes, 295 bricks (June 2016 file; current version only in the browser). | [GS1 GPC Automotive download](https://www.gs1.org/standards/gpc/gpc-automotive/jun-2016), [GPC browser](https://gpc-browser.gs1.org/) |
| Wikidata Q6405028 "automotive part": 60 items with `P279=Q6405028`, 46 with `P31=Q6405028`, 125 transitive subclasses. Structured data is CC0, and merged items become redirects. | measured via the Wikidata API and query service; [Wikidata:Licensing](https://www.wikidata.org/wiki/Wikidata:Licensing), [Help:Merge](https://www.wikidata.org/wiki/Help:Merge) |

## 3. Do part types depend on the vehicle?

Yes, in every taxonomy at a useful grain, though none makes the part type's
*name* vehicle-specific:

- **TecDoc** says so outright. Each generic article carries whether it may link
  to passenger cars, commercial vehicles, engines or axles (table 320), and
  table 340 restricts generic articles to engine types (no exhaust parts on an
  EV).
- **Hollander** keeps one global list of part types, but interchange coverage
  for a type starts at a model year (catalytic converter and torque converter
  from MY2022) and only exists where Hollander has written it. In a yard system
  a part type is used through (type, model, year).
- **PCdb** terminologies are vehicle-independent. ACES ties a (part type,
  position) to a VCdb BaseVehicle plus qualifiers in each application, and
  `CodeMaster` limits which positions a part type takes, not which vehicles.
- **NHTSA** components are recorded per recall per vehicle. Some systems only
  exist for some vehicle classes (`SERVICE BRAKES, AIR`; `EQUIPMENT ADAPTIVE/MOBILITY`).

For [#121](https://github.com/joshuasprow/nitrovin/issues/121): a Mapping
between part types can stand alone. Which part types *apply* to a vehicle is a
separate, vehicle-keyed fact that only the licensed sources carry.

## 4. Read: could any of these be a shippable spine?

None. A spine has to be fine enough that two yards' or catalogs' part types land
on the same entry, stable enough that mappings survive releases, and licensed
for nitrovin to commit. PCdb, Hollander and TecDoc have the grain and stable
integer ids, but all three are licensed. PCdb's licence names the ids and the
taxonomy as licensed property and limits distribution to other subscribers, so
nitrovin can't even publish a PCdb id list. The shippable ones each fail
another test. NHTSA's components are public domain but keyed by renameable
strings and shaped by safety defects (no fenders). HS/HTSUS and Google's
taxonomy are a dozen to thirty buckets. GPC is retail accessories. Wikidata has
125 unevenly curated classes anyone can rearrange. Shipping any of them as *the*
spine would make every user map into a taxonomy coarser than the one they
already have, and lose information in translation. So nitrovin should treat the
part-type spine as data the user supplies: a vocabulary declared as the spine,
whose ids nitrovin treats as opaque and namespaced (`pcdb:1684`,
`hollander:300`). For licensed spines, the format should carry ids without names
so that a subscriber's file never has to embed the licensed taxonomy. eBay's
leaf categories ([#118](https://github.com/joshuasprow/nitrovin/issues/118)) are
the one public candidate still open. A nitrovin-minted part-type list (like the
private map's vehicle extensions) is the other option, but nitrovin would then
own a taxonomy.

## 5. Gaps

- **TecDoc generic-article count**: not published on the public TecAlliance
  pages read. Closing it needs a TecDoc reference-data subscriber reading table
  320.
- **Current Hollander US part-type count** and the full code list: the only
  primary figure is 2014's "more than 160 active". Closing it needs a Hollander
  licensee (Snyder's Interchange install). It can't be published here.
- **Real PCdb ↔ Hollander fan-out**: needs both licensed datasets side by side.
  Closing it would be a private measurement (e.g. in the one-off Snyder smoke
  test), never committed.
- **Free VIP access to view PCdb**: Auto Care says VIP access "does not require
  a paid active subscription" but some products do. Whether the PCdb viewer is
  free needs an autocare.org account, which this research didn't create.
- **GPC and Google taxonomy redistribution terms**: no licence found on the
  pages and files read. Moot unless one is chosen as a spine.

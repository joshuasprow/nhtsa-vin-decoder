# eBay's vehicle compatibility and part categories: shape, ids, NHTSA keys, names against vPIC, redistribution terms

Resolves #118, a research ticket on the map #117. Investigated 2026-11-10 against eBay's
published OpenAPI contracts, the Trading API schema and guides, eBay's seller-centre
pages and files, the eBay Developers Program API License Agreement, and the vPIC
2026_10 artifact nitrovin embeds (`github.com/joshuasprow/nitrovin-data`
v1.202610.1). No eBay keys were used: every eBay call this doc describes needs an
OAuth token, so no live eBay data was read. Claims not observed are marked
[INFERENCE]. Terms from GLOSSARY.md: vocabulary, entry, spine, mapping,
translation.

## Answer in brief

- **Shape.** eBay's vehicle vocabulary is a set of **name/value pairs with no ids**. On
  eBay US Motors an entry is the tuple *(Year, Make, Model, Trim, Engine)*, each value
  a display string. Trim is "Body + Submodel + # of doors", and Engine is a long
  string such as `4.7L 285Cu. In. V8 GAS SOHC Naturally Aspirated`. The UK, DE and AU
  marketplaces add numeric **K types** (TecDoc's vehicle numbers). DE/UK
  motorcycles use **ePIDs** (eBay catalog product ids).
- **Ids and stability.** Make, model, trim and engine values carry no id. eBay bumps a
  `metadataVersion` "whenever there are compatibility name changes", so names change
  and nothing tracks the change. Category ids are numeric, are scoped to one
  marketplace's category tree, and come with a tree version and an
  expired→replacement map. eBay publishes no stability guarantee for ePIDs.
- **NHTSA.** eBay keys nothing on NHTSA. No eBay contract mentions vPIC, NHTSA or
  ACES. `VIN` appears only as a field on *vehicle* listings, never in parts
  compatibility. Translating eBay entries onto the vPIC spine means matching names.
- **Names against vPIC 2026_10.** eBay's own samples give 11 makes and 11 models.
  Makes: 7 exact, 0 more after case-folding, 4 unmatched. Models, within their make:
  6 exact, 0 more after case-folding, 5 unmatched. Every unmatched name is a vehicle
  vPIC does not have: pre-1981 cars, non-US models, a marque under a different name.
  The bulk counts for Ford, Chevrolet, Ram, Dodge, BMW and Lexus are a **gap**: they
  need an eBay app token.
- **Redistribution: no.** eBay owns anything "created or derived" from eBay
  Content. You may not "distribute … copy, store or modify eBay Content" beyond the
  licence. eBay Content is defined as eBay's Confidential Information. The
  downloadable Master Vehicle Lists may be used "exclusively for the purpose of
  matching vehicle models to your parts listing(s) on eBay". An eBay vocabulary can
  never ship in nitrovin, in a sibling module, or on nitrovin.app without eBay's
  written consent. Only one path is open: a user builds the vocabulary locally under
  their own eBay agreement (their own keys, or the seller MVL download) and loads it
  at runtime. Even then the user carries eBay's purpose, freshness and deletion
  terms.

## 1. The vehicle compatibility vocabulary

### Where it is served

| Surface | Calls | Request / response shape | Auth | Source |
| --- | --- | --- | --- | --- |
| Taxonomy API v1.1.1 | `GET /category_tree/{id}/get_compatibility_properties?category_id=` | `compatibilityProperties[]: {name, localizedName}` | application token, scope `api_scope` | [OpenAPI](https://developer.ebay.com/api-docs/master/commerce/taxonomy/openapi/3/commerce_taxonomy_v1_oas3.json), [docs](https://developer.ebay.com/develop/api/sell/taxonomy_api/category_tree/getcompatibilitypropertyvalues) |
| | `GET /category_tree/{id}/get_compatibility_property_values?category_id=&compatibility_property=&filter=` | `compatibilityPropertyValues[]: {value}`. Only a string. `filter=Year:2018,Make:Toyota,Model:Camry` narrows it; commas inside values are escaped `\,` | same | same |
| Metadata API v1.12.1 (`/compatibilities`) | `get_compatibility_property_names` | `propertyName` (canonical), `propertyDisplayName` (localized), `propertyNameMetadata.displaySequence`. Datasets: `Searchable`, `Sortable`, `DisplayableSearchResults`, `DisplayableProductDetails` | application token | [OpenAPI](https://developer.ebay.com/api-docs/master/sell/metadata/openapi/3/sell_metadata_v1_oas3.json) |
| | `get_compatibility_property_values`, `get_multi_compatibility_property_values` | `{propertyName, value}` pairs plus **`metadataVersion`**: "upticked whenever there are compatibility name changes for the specified marketplace" | same | same |
| | `get_product_compatibilities` | a product's fits as name/value pairs. The product is named by `epid`, `upc`, `ean`, `isbn` or `productId` | same | same |
| | `get_compatibilities_by_specification` | fits derived from part dimensions (tyres, rims), not from a vehicle entry | same | same |
| | `marketplace/{id}/get_automotive_parts_compatibility_policies` | per category: `compatibilityBasedOn` (`ASSEMBLY` or `SPECIFICATIONS`), `compatibleVehicleTypes`, `maxNumberOfCompatibleVehicles`. The US is `EBAY_MOTORS_US` | same | same |
| Inventory API 1.18.5 (write side) | `createOrReplaceProductCompatibility` | `compatibleProducts[]`: `compatibilityProperties` as `{name, value}`, *or* `productIdentifier: {epid \| ktype \| gtin}`, which "will automatically be populated" into make/model/year/trim/engine. `productFamilyProperties` is deprecated | user token | [OpenAPI](https://developer.ebay.com/api-docs/master/sell/inventory/openapi/3/sell_inventory_v1_oas3.json) |
| Trading API (XSD v1489) | `ItemCompatibilityList` in the `AddItem` family | `Compatibility/NameValueList` with `Year`/`Make`/`Model`[/`Trim`/`Engine`], or `KType`, or `ePID` | user token | [guide](https://developer.ebay.com/api-docs/user-guides/static/trading-user-guide/manually-specify-compatibility.html), [XSD](https://developer.ebay.com/webservices/latest/ebaySvc.xsd) |
| Browse API v1.20.4 (buy side) | `item_summary/search?compatibility_filter=Year:2018;Make:BMW`, `item/{id}/check_compatibility` | the same names. Cars, trucks and motorcycles only | application token | [OpenAPI](https://developer.ebay.com/api-docs/master/buy/browse/openapi/3/buy_browse_v1_oas3.json) |
| Catalog API v1_beta.5.3 | `product/{epid}`, `product_summary/search` | catalog products by ePID (parts, and the vehicles that ePIDs identify) | user token or `commerce.catalog.readonly` | [OpenAPI](https://developer.ebay.com/api-docs/master/commerce/catalog/openapi/3/commerce_catalog_v1_beta_oas3.json) |
| Master Vehicle List (MVL) files | download | US: behind seller login. UK: public `.xlsx`, password printed on the page, "updated quarterly" | eBay account (US) | [KB 5064](https://developer.ebay.com/support/knowledge-base/5064), [US](https://www.ebay.com/sellercenter/listings/list-with-fitment), [UK](https://www.ebay.co.uk/sellercentre/selling/motors/sell-motors-parts) |

The older Product Metadata API (`getCompatibilitySearchValuesBulk` and the like) "has
been deprecated and decommissioned"
([guide](https://developer.ebay.com/api-docs/user-guides/static/trading-user-guide/manually-specify-compatibility.html)).

### What an entry looks like

- **US Motors (tree 100).** eBay's Taxonomy quick start shows
  `get_compatibility_properties` for category 33743 answering exactly `Year`, `Make`,
  `Model`, `Trim`, `Engine`
  ([eBay Partner Help](https://partnerhelp.ebay.com/helpcenter/s/article/Taxonomy-API-Quick-Start-Guide)).
  `displaySequence` gives the hierarchy: "Engine is subordinate to Trim, which is
  subordinate to Model". A listing needs at least Year, Make and Model. Trim and
  Engine are optional; leaving them out fans out to every trim and engine
  ([guide](https://developer.ebay.com/api-docs/user-guides/static/trading-user-guide/manually-specify-compatibility.html);
  [US seller centre FAQ](https://www.ebay.com/sellercenter/listings/list-with-fitment)).
  The FAQ defines each field:

  | Field | eBay's definition | eBay's example |
  | --- | --- | --- |
  | Year | Year of manufacture | 2005 |
  | Make | Auto assembler | Dodge |
  | Model | — | Durango |
  | Trim | Body + Submodel + # of doors | Limited Sport Utility 4-Door |
  | Engine | size (litres, cc, cid), cylinders, block type, fuel type, cylinder head type | 4.7L 285Cu. In. V8 GAS SOHC Naturally Aspirated |

  An entry is therefore **one year**, not a year range. A part that fits 2006–2009 is
  four entries (the Acura TL sample in the guide).
- **K-type marketplaces (AU, DE, UK, FR, IT, ES).** A K type is "a vehicle
  specification numbering system provided by TecDoc Information Systems". One number
  bundles "make, model, platform, type, engine, and production period". eBay's
  example: 25456 = Audi A4, platform 8K2, type 1.8 TFSI, 88 kW/120 PS/1798 cc,
  2008/01–. A K type "typically corresponds to a specific production period" and
  counts as one compatibility per year
  ([guide](https://developer.ebay.com/api-docs/user-guides/static/trading-user-guide/manually-specify-compatibility.html)).
  The UK and AU seller pages require at least **Make, Model, Variant**, not Year
  ([UK](https://www.ebay.co.uk/sellercentre/selling/motors/sell-motors-parts),
  [AU](https://www.ebay.com.au/sellercentre/vehicle-parts-compatibility)).
  eBay's Taxonomy filter example uses a `BodyStyle` property (`AWD B9 8W5\,8WD`),
  which US entries do not have.
- **DE/UK motorcycles.** These are named by ePID, taken from eBay's Master Motorcycle
  List (MML). An ePID stands for make, model, CCM, year and submodel (same guide).
- **Limits.** 1,000 or 3,000 vehicles per listing, depending on the category (AU
  page). The UK allows 3,000. `get_automotive_parts_compatibility_policies` carries
  the per-category `maxNumberOfCompatibleVehicles`.

## 2. Ids and how stable they are

| Thing | Id? | Stability evidence |
| --- | --- | --- |
| Make, Model, Trim, Engine, Year values | **None.** Only the string `value`; the entry key is the name tuple | `metadataVersion` is "upticked whenever there are compatibility name changes" (Metadata API). The API publishes no rename log or old→new map. A renamed value just stops matching |
| Property names (`Year`, `Make`, …) | `propertyName` (canonical) vs `propertyDisplayName` (localized) | They "will vary based on the eBay marketplace and the eBay category" (Taxonomy, Metadata) |
| K type | numeric, owned by TecDoc | eBay calls it the most accurate key. Its lifecycle belongs to TecDoc, not eBay. It is licensed third-party data (§5) |
| ePID | numeric string (`81391542` is a headlight in eBay's sample; `128653462` a motorcycle in the MML sample) | **Gap:** no published guarantee that an ePID survives catalog merges. The Catalog API is still `v1_beta` |
| MVL releases | file names carry a date (UK `UK_MVL_2026_04.xlsx`, `UK_MML_2026_06.xlsx`) | UK: "updated quarterly", with sign-up for change emails. eBay's 2010 fitment deck: "Catalog and MVL gets updated at a specific interval (sometimes even outside the interval)" ([PDF](https://pics.ebaystatic.com/aw/pics/motors/compatibility/download/pdf/GetOntoFitment_Using_APIs.pdf)) |
| Category | `categoryId`, numeric string, "unique … within its category tree" | Trees carry `categoryTreeVersion` ("cache this value … to determine if this category tree has been modified"). `get_expired_categories` maps expired leaves to active ones (merges and splits), but "does not return information about expired categories that have no corresponding active categories" (Taxonomy) |

In short, nothing on the vehicle side has an eBay-owned, stable numeric id outside
the ePID/K-type layers. A vocabulary entry built from eBay is identified by its
strings plus the marketplace and the `metadataVersion` or MVL date it was read at.

## 3. Does eBay key on anything NHTSA?

No.

- Counted in eBay's contracts: `nhtsa` and `vpic` occur 0 times in the Taxonomy,
  Metadata, Inventory, Catalog and Browse OpenAPI files and in the Trading XSD.
  `ACES` also occurs 0 times.
- `VIN` occurs only in the Trading schema, as `Item.VIN` and `VINLink` for listings
  *of vehicles* in US Motors Cars & Trucks (6001), Motorcycles, Commercial Trucks and
  similar categories. It is "required for cars and trucks from model year 1981 and
  later". The UK equivalent is `VRM`. Neither appears in parts compatibility:
  `ItemCompatibilityList`, Inventory's `compatibleProducts` and Browse's
  `compatibility_filter` take only names, K types or ePIDs.
- eBay does not say where the US MVL comes from. The 2010 deck names part catalogs
  ("Activant, DCI and Gecko byte") and says eBay "maintains a list of all the
  vehicles manufactured after 1896". The engine strings are in ACES VCdb style
  [INFERENCE: format resemblance only, no eBay statement].

So an eBay → vPIC mapping is a name match plus whatever curation the vocabulary's
author does. Nothing in eBay's data points at a vPIC `MakeID` or `ModelID`.

## 4. Marketplaces

| Marketplace | Tree id | Vehicle key | Notes |
| --- | --- | --- | --- |
| eBay Motors US | 100 | Year/Make/Model/Trim/Engine names; ePID for catalog parts | `EBAY_MOTORS_US` in Metadata. P&A categories need tree 100, not 0 |
| eBay US | 0 | same vocabulary, in non-Motors categories | Taxonomy: "eBay US (Motors and non-Motors categories)" |
| eBay Canada | 2 | names | in Trading's list of parts-compatibility sites |
| eBay UK | 3 | K type; Make/Model/Variant (+BodyStyle, Type, Year, Engine); ePID for motorcycles | MVL quarterly, public download with password |
| eBay Germany | 77 | K type; ePID for motorcycles | — |
| eBay Australia | 15 | K type; Make/Model/Variant | public category fitment file (§6) |
| eBay France, Italy, Spain | 71, 101, 186 | K type | Taxonomy and K-type lists only |

Sources: Taxonomy `category_tree_id` docs, the
[API support page](https://developer.ebay.com/api-docs/user-guides/static/trading-user-guide/compatible-parts-api-support.html)
and the manual-compatibility guide. The UK MVL's columns are Make, Model, Variant,
BodyStyle, Type, Year, Engine, K-Type, plus an additions/updates flag and a deletions
sheet. This research opened that file to read its terms (§7) and headers, used none
of its rows, and deleted it. A US entry and a UK entry for the same car share no key.
Make and model strings may coincide, but the UK splits by variant and engine code
under a K type, and the US splits by trim and engine string under a year.

## 5. Parts & accessories categories

eBay's part "types" are the leaf categories of each marketplace's category tree. They
come from `getCategoryTree` / `getCategorySubtree`: nodes of `{categoryId,
categoryName}`, `categoryTreeNodeLevel`, `leafCategoryTreeNode` and children, all
under `categoryTreeVersion` (Taxonomy). Those calls need a token. eBay Australia
publishes its whole Vehicle Parts & Accessories subtree with fitment status as a
public spreadsheet
([`category-fitment-availability_AU.xlsx`](https://p.ebaystatic.com/aw/pics/au/selling/category-fitment-availability_AU.xlsx),
linked from the [AU seller centre](https://www.ebay.com.au/sellercentre/vehicle-parts-compatibility)).
Its shape, measured 2026-11-10:

| Measure | Value |
| --- | ---: |
| Categories under Vehicle Parts & Accessories [131090] | 1,622 |
| Leaves | 1,450 |
| Depth (root = 1): 2 / 3 / 4 / 5 | 21 / 191 / 1,256 / 153 |
| Fitment enabled: all / leaves | 809 / 726 |
| Enabled, by level-2 branch | Car & Truck Parts & Accessories [6030] 419, Motorcycle Parts [10063] 187, Scooter Parts [84149] 168, Motorcycle Accessories 11, Car Performance Parts 9, Car & Truck Manuals & Literature 6, Motorcycle Manuals & Literature 6, Vehicle Electronics & GPS 3 |

A typical path is Car & Truck Parts & Accessories [6030] > Brakes & Brake Parts
[33559] > Brake Pads [57357]. Points that matter for a part-type spine (#119, #121):

- The tree mixes part types with catch-alls ("Other Brake Parts" [42605]), literature
  and electronics, and splits one part by vehicle class: brake pads are 57357 (car),
  177925 (motorcycle) and 182263 (scooter).
- Names are localized per tree: AU motorcycle "Callipers & Parts" sits beside car
  "Calipers & Brackets". Ids are only unique within a tree.
  [INFERENCE] The same numbers (6030, 33559, 33707) appear in US docs, so the trees
  share ids where categories line up, but the API promises nothing across trees.
- Ids churn. Recent splits show up as 262xxx ids, and expired categories map forward
  only when eBay records a merge or split (§2).
- Gap: the US Motors tree (100) was not fetched; it needs a token.

## 6. eBay names against vPIC names

### Method

- **vPIC side:** the `lookup` rows for `make` (12,387 names) and `model` (32,070
  names), plus `make_model` (32,070 rows), read from the embedded 2026_10 artifact.
  The live API (`GetModelsForMake/{ford,chevrolet,ram,dodge,bmw,lexus,alfa romeo}`,
  2026-11-10) agrees with 2026_10 for every make id it returned. The one difference:
  Lexus has 18 models live against 17 in 2026_10 (live adds `TZ`, model id 38853).
  vPIC writes these marques in mixed case (`Ford`, `Alfa Romeo`, `BMW`). Across all
  of vPIC, 3,339 make and 14,251 model names are all-caps, so case-folding matters
  overall, though not for this sample.
- **eBay side:** only names eBay itself published for US Motors: the Taxonomy quick
  start (the first six makes of category 33743's Make list; the first six Alfa Romeo
  models), the Ferrari Trim sample, and the guide and API examples (Toyota Camry,
  Honda Accord and Civic, Acura TL, Dodge Durango, BMW). This research did not
  scrape eBay's site, which eBay's
  [User Agreement](https://www.ebay.com/help/policies/member-behaviour-policies/user-agreement?id=4259)
  forbids ("any robot, spider, scraper … or other automated means"). It also did not
  use the UK MVL, whose terms forbid other uses (§7).
- **Match:** *exact* compares bytes. *Case-folded* compares after trimming, collapsing
  spaces and Unicode case-folding, the normalization the map fixed. Models match only
  under the matched make's id.

### Result

| eBay make | vPIC 2026_10 | Result |
| --- | --- | --- |
| Acura, Alfa Romeo, BMW, Dodge, Ferrari, Honda, Toyota | 475, 493, 452, 476, 603, 474, 448 | exact |
| Abarth | — | unmatched (no vPIC make) |
| AC | — (vPIC has `AC Customs`, `AC Propulsion`, `AC Scott Trailers`) | unmatched |
| Advance Mixer | — (vPIC has `Terex Advance Mixer`) | unmatched; same maker, different name |
| Allard | — (vPIC has `ALLARD MOTOR WORKS`) | unmatched; [INFERENCE] probably not the same vehicles |

| eBay make / model | vPIC model id | Result |
| --- | --- | --- |
| Alfa Romeo 4C · Toyota Camry · Honda Accord · Honda Civic · Acura TL · Dodge Durango | 2035 · 2469 · 1861 · 1863 · 1873 · 3508 | exact |
| Alfa Romeo 147, 8C 2900, Alfetta, Berlina, Brera | — (vPIC Alfa Romeo has 164, 4C, 8C Competizione Spider, GTV6, Giulia (952), Milano, Spider, Stelvio, TONALE) | unmatched |

| | Exact | Case-folded only | Unmatched | Total |
| --- | ---: | ---: | ---: | ---: |
| Makes | 7 | 0 | 4 | 11 |
| Models (make-scoped) | 6 | 0 | 5 | 11 |

Reading it: case-folding added nothing here, because eBay and vPIC both use mixed case
for these marques. Every miss is a vehicle vPIC lacks: non-US models (147, Brera),
pre-1981 cars (8C 2900, Allard, AC), or a marque vPIC files under a longer company
name (Terex Advance Mixer). Those are exactly the "extensions" and author-side
mappings the map already expects. The sample is small and alphabetically front-loaded
(A-makes), so it is no estimate of bulk match rates.

### Gap: bulk counts for Ford, Chevrolet, Ram/Dodge, BMW, Lexus

eBay publishes no model list for these makes that can be read without an account
(the US MVL needs a seller login, and the API needs a token). The vPIC side is ready.
2026_10 has 149 Ford (460), 124 Chevrolet (467), 11 Ram (496), 51 Dodge (476), 265
BMW (452) and 17 Lexus (515) models. **The task that closes it:** a maintainer with
their own eBay developer account calls `get_compatibility_property_values`
(tree 100, category 6030, `Make`, then `Model` filtered per make). They run the same
make-scoped match locally against the artifact, publish only the counts and a few
illustrative pairs, and delete the pulled values afterwards (the licence's
intermediate-copies rule, §7). Hypotheses that run should test, all [INFERENCE] and
unverified here:

- vPIC files Lexus by series (`ES`, `RX`), while eBay may list engine-suffixed models
  (`ES300`, `RX350`).
- vPIC files Ram and GM pickup tonnage as series, while eBay may carry it in the model
  (`1500`). This is the private map's "tonnage class".
- Ram trucks before the 2011 brand split will sit under Dodge.

## 7. Terms: can an eBay-derived vocabulary be redistributed?

### eBay Developers Program Terms of Use and API License Agreement

Source: [developer.ebay.com/join/api-license-agreement](https://developer.ebay.com/join/api-license-agreement/),
read 2026-11-10. The page shows no version date. Section numbers come from the
agreement's own cross-references.

- **Licence scope (§3.1, Authorized Use):** the licence is "non-exclusive,
  non-transferable, non-sublicensable … solely for the purpose of facilitating your
  own or Your Users' use of eBay Services". It allows "limited intermediate copies of
  eBay Content only as necessary … All intermediate copies must be deleted when they
  are no longer required".
- **Definitions:** "eBay Content" means "all of the information, data, content … stored
  by and retrieved from eBay". Compatibility values fall under it.
- **Sublicensing (§4.2):** the only right you may sublicense is display, "solely to
  enable them to display eBay Content … through your Application". Your Users get "no
  programmatic control over any API".
- **Ownership (§5):** eBay owns eBay Content "and shall own any content created or
  derived therefrom or any form of derivative works". A vocabulary built from
  eBay's names is such a derivative.
- **Freshness (§8, eBay Content):** "other eBay Content must be no more than
  twenty-four (24) hours older than content displayed on the eBay Site". This binds
  any displayed translation drawn from a cached vocabulary.
- **Restricted Activities:** "without eBay's prior written consent" you may not
  "Distribute, publish, or allow access or linking to eBay Services, including any
  API, from any location or source other than your Application", nor "Sell, rent,
  trade, distribute, lease (or otherwise commercialize), copy, store or modify eBay
  Content, other than for the purposes allowed".
- **Termination (§16.2):** "destroy all copies of, or anything containing, eBay
  Content … within ten (10) days".
- **Confidentiality (§17):** "Confidential Information" includes "all information and
  data eBay provides to you under this API License Agreement, including … eBay
  Content". You "will not use or disclose" it beyond the Terms.

### Master Vehicle List downloads

- US ([seller centre](https://www.ebay.com/sellercenter/listings/list-with-fitment)):
  "By downloading and opening any of the files listed below including the Parts,
  Tires, or Rims Catalog, Master Vehicle Lists (MVL), Master Powersports Vehicle
  Lists (MPSOV) and Master Boat and Component Lists (Boat Engines) you agree to use
  the data exclusively for the purpose of matching vehicle models to your parts
  listing(s) on eBay Motors."
- UK (inside `UK_MVL_2026_04.xlsx`, sheet "Agreement"): "By using the password and
  opening the downloadable master vehicle list (MVL), you agree to use the MVL data
  exclusively for the purpose of matching vehicle models to your new parts listing(s)
  on eBay Motors. Any further use or dissemination of the data for purposes other
  than listing on eBay is prohibited." The password is printed on a public page, but
  opening the file binds the reader to these terms. That is why §6 uses none of it.
- K types are TecDoc's numbering ("provided by TecDoc Information Systems"). A
  K-type-keyed vocabulary would also carry a third party's rights, as Hollander's
  does. [INFERENCE] TecDoc's own licence terms were not read; they would need their
  own check before any K-type vocabulary is handled.

### Verdict

- **Ship in nitrovin's open-source release, a public data module, or nitrovin.app:
  no.** It would be distribution of eBay Content and of a derivative eBay owns,
  outside any Application's licensed use. Only eBay's prior written consent changes
  that. The map's note that the format "can carry a public one (eBay the likely
  first)" should drop eBay as the likely first *shipped* vocabulary.
- **Build locally and load at runtime: yes, under the user's own agreement.** A user
  with their own eBay developer keys can pull the values, or a seller can download the
  MVL. They write a vocabulary file and load it like any other. The terms stay with
  the user, not nitrovin: the use must facilitate eBay Services (for a seller,
  matching vehicles to their own eBay parts listings is the stated purpose), copies
  must be refreshed or deleted, and nothing may be republished. nitrovin needs no eBay
  code for this, and an authoring tool is out of the map's scope anyway.

## 8. What this means for the open tickets

- **#120 (what a mapping says):** an eBay entry key is a *tuple of strings* under
  marketplace-specific property names. US uses Year/Make/Model/Trim/Engine; UK uses
  Make/Model/Variant/BodyStyle/Type/Year/Engine, optionally with a K type, and
  ePIDs appear for some. Entries are one year each (US) or a production period (K
  type), which fits year-scoped mappings. Trim, Engine and Variant are finer than a
  vPIC model, so eBay→spine is many-to-one and spine→eBay is one-to-many.
- **#121 / #119 (part-type spine):** eBay categories are marketplace-scoped, localized,
  versioned and churning, and they mix part types with catch-alls and literature.
  Even unlicensed they would be a poor neutral spine, and they cannot ship.
- **#122 (vocabulary file):** values carry no ids and eBay renames without a map, so
  a file needs source identity: marketplace, category tree id and version,
  `metadataVersion` or MVL date, and retrieval date. A stale entry then shows up as a
  name that no longer resolves. The format must allow free-form composite string
  keys and optional external ids (K type, ePID).

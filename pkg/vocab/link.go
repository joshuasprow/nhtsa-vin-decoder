package vocab

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
)

// Link checks files against each other, as loaded together: one name per
// file across the kinds, every extension reference defined by a loaded
// extension set (and naming an extension make where a make is meant),
// every part vocabulary's spine loaded and every id it maps to in that
// spine, and at most one catalog, its spine and vehicle vocabulary loaded,
// its part types in that spine and its vehicles named by exactly that
// vocabulary's fields. The error is Faults.
func Link(files []*File) error {
	var faults Faults
	byName := map[string]*File{}
	for _, f := range files {
		if g, dup := byName[f.Name]; dup {
			faults = append(faults, fault(f.Path, 1, "/name: %q is also the name of %s", f.Name, g.Path))
			continue
		}
		byName[f.Name] = f
	}
	var catalog *File
	// ext resolves an extension reference, or says why it does not.
	ext := func(ref string) (*Extension, string) {
		set, id := splitExt(ref)
		s := byName[set]
		switch {
		case s == nil:
			return nil, "extension set " + strconv.Quote(set) + " is not loaded"
		case s.Kind != KindExtensionSet:
			return nil, strconv.Quote(set) + " is " + s.Kind.noun() + ", not an extension set"
		}
		x, ok := s.Extension(id)
		if !ok {
			return nil, "extension set " + strconv.Quote(set) + " has no id " + strconv.Quote(id)
		}
		return x, ""
	}
	for _, f := range files {
		switch f.Kind {
		case KindVocabulary:
			var spine *File
			if f.Subject == SubjectPart {
				switch spine = byName[f.Spine]; {
				case spine == nil:
					faults = append(faults, fault(f.Path, 1, "/spine: spine %q is not loaded", f.Spine))
					continue
				case spine.Kind != KindSpine:
					faults = append(faults, fault(f.Path, 1, "/spine: %q is %s, not a spine", f.Spine, spine.Kind.noun()))
					continue
				}
			}
			for i := range f.Entries {
				e := &f.Entries[i]
				for j := range e.Mappings {
					switch mp := &e.Mappings[j]; {
					case mp.Ext != "":
						if _, why := ext(mp.Ext); why != "" {
							faults = append(faults, fault(f.Path, e.Line, "/mappings/%d/ext: %s", j, why))
						}
					case mp.ID != "":
						if _, ok := spine.SpineEntry(mp.ID); !ok {
							faults = append(faults, fault(f.Path, e.Line, "/mappings/%d/id: spine %q has no id %q", j, f.Spine, mp.ID))
						}
					}
				}
			}
		case KindExtensionSet:
			for _, x := range f.Extensions {
				if x.MakeExt == "" {
					continue
				}
				switch mk, why := ext(x.MakeExt); {
				case why != "":
					faults = append(faults, fault(f.Path, x.Line, "/make: %s", why))
				case !mk.IsMake():
					faults = append(faults, fault(f.Path, x.Line, "/make: %s is an extension model, not a make", x.MakeExt))
				}
			}
		case KindCatalog:
			if catalog != nil {
				faults = append(faults, fault(f.Path, 1, "/kind: %s is a catalog too: a deployment loads one catalog", catalog.Path))
				continue
			}
			catalog = f
			faults = append(faults, linkCatalog(f, byName, ext)...)
		}
	}
	if len(faults) > 0 {
		return faults
	}
	return nil
}

// linkCatalog checks catalog f against the files loaded with it, byName:
// its spine and vehicle vocabulary loaded, every part type of its
// fitments and redirects in that spine, and every vehicle named by
// exactly that vocabulary's fields, or by an extension a loaded set
// defines. Its faults come in line order. A compiled catalog, checked line
// by line when it was compiled, is checked by its dictionary: each part
// type and vehicle once, about the file as a whole.
func linkCatalog(f *File, byName map[string]*File, ext func(string) (*Extension, string)) Faults {
	var faults Faults
	header := 1 // the header's line
	if f.compiled != nil {
		header = 0
	}
	spine := byName[f.Spine]
	switch {
	case spine == nil:
		faults = append(faults, fault(f.Path, header, "/spine: spine %q is not loaded", f.Spine))
	case spine.Kind != KindSpine:
		faults = append(faults, fault(f.Path, header, "/spine: %q is %s, not a spine", f.Spine, spine.Kind.noun()))
		spine = nil
	}
	var vehicles *File
	if f.Vehicles != Reserved {
		switch vehicles = byName[f.Vehicles]; {
		case vehicles == nil:
			faults = append(faults, fault(f.Path, header, "/vehicles: vocabulary %q is not loaded", f.Vehicles))
		case vehicles.Kind != KindVocabulary:
			faults = append(faults, fault(f.Path, header, "/vehicles: %q is %s, not a vehicle vocabulary", f.Vehicles, vehicles.Kind.noun()))
			vehicles = nil
		case vehicles.Subject != SubjectVehicle:
			faults = append(faults, fault(f.Path, header, "/vehicles: %q is a part vocabulary, not a vehicle vocabulary", f.Vehicles))
			vehicles = nil
		}
	}
	if f.compiled != nil {
		return append(faults, f.compiled.link(spine, vehicles, ext)...)
	}
	// why is, per vehicle that does not resolve, why not: once each,
	// however many lines name it.
	why := map[*Vehicle]string{}
	for _, v := range f.catalog.vehicleList {
		if w := vehicleWhy(v, vehicles, ext); w != "" {
			why[v] = w
		}
	}
	typeOK := func(id string) bool {
		_, ok := spine.SpineEntry(id)
		return ok
	}
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		if spine != nil && !typeOK(fit.Key.Type) {
			faults = append(faults, fault(f.Path, fit.Line, "/fitment/type: spine %q has no id %q", f.Spine, fit.Key.Type))
		}
		if len(why) == 0 {
			continue
		}
		for j := range fit.Applications {
			if w, bad := why[fit.Applications[j].Vehicle]; bad {
				faults = append(faults, fault(f.Path, fit.Line, "/applications/%d/vehicle%s", j, w))
			}
		}
	}
	for i := range f.Redirects {
		rd := &f.Redirects[i]
		for _, t := range []struct{ ptr, id string }{{"from", rd.From}, {"to", rd.To}} {
			if spine != nil && !typeOK(t.id) {
				faults = append(faults, fault(f.Path, rd.Line, "/redirect/%s: spine %q has no id %q", t.ptr, f.Spine, t.id))
			}
		}
		if w, bad := why[rd.Vehicle]; bad {
			faults = append(faults, fault(f.Path, rd.Line, "/redirect/vehicle%s", w))
		}
	}
	slices.SortStableFunc(faults, func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
	return faults
}

// vehicleWhy is why vehicle v of a catalog does not resolve against its
// vehicle vocabulary vehicles (nil when it is vpic or not loaded) and the
// extension sets ext reads, as a pointer's tail and a reason; "" when it
// does.
func vehicleWhy(v *Vehicle, vehicles *File, ext func(string) (*Extension, string)) string {
	switch {
	case v.Ext != "":
		if _, w := ext(v.Ext); w != "" {
			return "/ext: " + w
		}
	case vehicles != nil:
		names := slices.Sorted(maps.Keys(v.Entry))
		if want := slices.Sorted(slices.Values(vehicles.Fields)); !slices.Equal(names, want) {
			return ": fields " + quoted(names) + " are not vocabulary " + strconv.Quote(vehicles.Name) + "'s: " + quoted(vehicles.Fields)
		}
	}
	return ""
}

// link checks a compiled catalog against its spine and vehicle vocabulary
// as loaded (nil when not, or when its vehicles are vpic): each part type
// a fitment or redirect names in the spine, and each vehicle resolving.
// They did against the files it was compiled with; a fault here is a file
// loaded beside it that is not one of those.
func (c *compiled) link(spine, vehicles *File, ext func(string) (*Extension, string)) Faults {
	var faults Faults
	for _, t := range c.types {
		if spine == nil || !t.named {
			continue
		}
		if _, ok := spine.SpineEntry(t.name); !ok {
			faults = append(faults, fault(c.path, 0, "part type %q: spine %q has no id %q, and had when the catalog was compiled", t.name, spine.Name, t.name))
		}
	}
	for _, v := range c.vehicles {
		if w := vehicleWhy(v, vehicles, ext); w != "" {
			faults = append(faults, fault(c.path, 0, "vehicle %s%s", vehicleText(v), w))
		}
	}
	return faults
}

// vehicleText is a catalog's vehicle as a file writes it.
func vehicleText(v *Vehicle) string {
	switch {
	case v.Entry != nil:
		return entryText(v.Entry)
	case v.Make != 0:
		return `{"make":` + strconv.Itoa(int(v.Make)) + "}"
	case v.Model != 0:
		return `{"model":` + strconv.Itoa(int(v.Model)) + "}"
	}
	return `{"ext":` + strconv.Quote(v.Ext) + "}"
}

package vocab

import "strconv"

// Link checks files against each other, as loaded together: one name per
// file across the three kinds, every extension reference defined by a
// loaded extension set (and naming an extension make where a make is
// meant), every part vocabulary's spine loaded and every id it maps to in
// that spine. The error is Faults.
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
			for _, e := range f.Entries {
				for i, mp := range e.Mappings {
					ptr := "/mappings/" + strconv.Itoa(i)
					switch {
					case mp.Ext != "":
						if _, why := ext(mp.Ext); why != "" {
							faults = append(faults, fault(f.Path, e.Line, "%s/ext: %s", ptr, why))
						}
					case mp.ID != "":
						if _, ok := spine.SpineEntry(mp.ID); !ok {
							faults = append(faults, fault(f.Path, e.Line, "%s/id: spine %q has no id %q", ptr, f.Spine, mp.ID))
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
		}
	}
	if len(faults) > 0 {
		return faults
	}
	return nil
}

package diet

import domaindiet "github.com/future-architect/uzomuzo-oss/internal/domain/diet"

func inheritedTypeCoupling(purls []string, coupling map[string]*domaindiet.CouplingAnalysis) domaindiet.CouplingAnalysis {
	if coupling == nil {
		return domaindiet.CouplingAnalysis{Unanalyzed: true}
	}
	result := domaindiet.CouplingAnalysis{IsUnused: true}
	for _, purl := range purls {
		if c := coupling[purl]; c != nil {
			if c.Unanalyzed {
				result = domaindiet.CouplingAnalysis{Unanalyzed: true}
			} else if !c.IsUnused {
				return *c
			}
		}
	}
	return result
}

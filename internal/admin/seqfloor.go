package admin

import (
	"context"

	"github.com/egerkuzma/emtds/internal/config"
)

func (s *Server) groupList() ([]config.Group, error) {
	if s == nil || s.cfg.Groups == nil {
		return nil, nil
	}
	return s.cfg.Groups.List(context.Background())
}

func (s *Server) networkFloor() (int, error) {
	var ids []string
	if s.cfg.Networks != nil {
		list, err := s.cfg.Networks.List(context.Background())
		if err != nil {
			return 0, err
		}
		for _, n := range list {
			ids = append(ids, n.ID)
		}
	}
	if s.cfg.Offers != nil {
		list, err := s.cfg.Offers.List(context.Background())
		if err != nil {
			return 0, err
		}
		for _, o := range list {
			ids = append(ids, o.NetworkID)
		}
	}
	return numericFloor(ids), nil
}

func (s *Server) sourceFloor() (int, error) {
	var ids []string
	if s.cfg.Sources != nil {
		list, err := s.cfg.Sources.List(context.Background())
		if err != nil {
			return 0, err
		}
		for _, n := range list {
			ids = append(ids, n.ID)
		}
	}
	groups, err := s.groupList()
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		ids = append(ids, g.SourceID)
	}
	return numericFloor(ids), nil
}

func (s *Server) offerFloor() (int, error) {
	var ids []string
	if s.cfg.Offers != nil {
		list, err := s.cfg.Offers.List(context.Background())
		if err != nil {
			return 0, err
		}
		for _, o := range list {
			ids = append(ids, o.ID)
		}
	}
	groups, err := s.groupList()
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		for _, st := range g.Streams {
			for _, ref := range st.Out.Offers {
				ids = append(ids, ref.ID)
			}
		}
	}
	return numericFloor(ids), nil
}

func (s *Server) landingFloor() (int, error) {
	var ids []string
	if s.cfg.Landings != nil {
		list, err := s.cfg.Landings.List(context.Background())
		if err != nil {
			return 0, err
		}
		for _, o := range list {
			ids = append(ids, o.ID)
		}
	}
	groups, err := s.groupList()
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		for _, st := range g.Streams {
			for _, ref := range st.Out.Landings {
				ids = append(ids, ref.ID)
			}
		}
	}
	return numericFloor(ids), nil
}

func (s *Server) domainFloor() (int, error) {
	var ids []string
	if s.cfg.Domains != nil {
		list, err := s.cfg.Domains.List(context.Background())
		if err != nil {
			return 0, err
		}
		for _, d := range list {
			ids = append(ids, d.ID)
		}
	}
	groups, err := s.groupList()
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		ids = append(ids, g.DomainID)
	}
	return numericFloor(ids), nil
}

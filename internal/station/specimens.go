package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) RegisterSpecimen(
	ctx context.Context,
	request contracts.RegisterSpecimenRequest,
) (domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return domain.Specimen{}, err
	}

	var created domain.Specimen
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		value, err := domain.NewSpecimen(
			s.nextID("specimen"),
			request.CatalogNumber,
			request.FieldLabel,
			request.TaxonHint,
			request.MatrixKind,
			request.JacketNotes,
			s.now(),
		)
		if err != nil {
			return err
		}
		if _, exists := m.FindSpecimenByCatalog(value.CatalogNumber); exists {
			return contracts.Conflict("duplicate_catalog_number", "目录号已存在，不能覆盖原标本")
		}
		m.PutSpecimen(value)
		created = value
		return nil
	})
	return created, err
}

type ConsignmentView struct {
	SpecimenID string               `json:"specimen_id"`
	Custody    domain.CustodyState  `json:"custody"`
	Current    *domain.Consignment  `json:"current,omitempty"`
	History    []domain.Consignment `json:"history"`
}

func (s *Service) DispatchConsignment(
	ctx context.Context,
	specimenID string,
	request contracts.DispatchConsignmentRequest,
) (domain.Consignment, error) {
	if err := ctx.Err(); err != nil {
		return domain.Consignment{}, err
	}

	var created domain.Consignment
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		specimen, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		if replay, ok := m.FindConsignmentByRequest(specimenID, request.RequestID); ok {
			created = replay
			return nil
		}
		value, err := domain.NewConsignment(
			s.nextID("consignment"),
			specimen,
			request.RequestID,
			request.Vendor,
			request.Reason,
			request.DispatchedBy,
			request.ExpectedBackOn,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.PutConsignment(value)
		created = value
		return nil
	})
	return created, err
}

func (s *Service) ReturnConsignment(
	ctx context.Context,
	specimenID string,
	consignmentID string,
	request contracts.ReturnConsignmentRequest,
) (domain.Consignment, error) {
	if err := ctx.Err(); err != nil {
		return domain.Consignment{}, err
	}

	var returned domain.Consignment
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		value, ok := m.GetConsignment(consignmentID)
		if !ok {
			return notFoundConsignment(consignmentID)
		}
		if err := value.Close(request.AcceptedBy, request.Conclusion, s.now()); err != nil {
			return err
		}
		m.PutConsignment(value)
		returned = value
		return nil
	})
	return returned, err
}

func (s *Service) ConsignmentsForSpecimen(
	ctx context.Context,
	specimenID string,
) (ConsignmentView, error) {
	if err := ctx.Err(); err != nil {
		return ConsignmentView{}, err
	}

	var view ConsignmentView
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		view = ConsignmentView{
			SpecimenID: specimenID,
			Custody:    domain.CustodyStation,
			History:    m.ConsignmentsForSpecimen(specimenID),
		}
		if current, ok := m.OpenConsignmentForSpecimen(specimenID); ok {
			value := current
			view.Custody = domain.CustodyConsignment
			view.Current = &value
		}
		return nil
	})
	return view, err
}

func notFoundConsignment(id string) error {
	return contracts.NotFound("consignment_not_found", "未找到外协委托 "+id)
}

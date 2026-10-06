package domain

import (
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

type SpecimenStatus string

const (
	SpecimenRegistered SpecimenStatus = "registered"
	SpecimenPlanned    SpecimenStatus = "planned"
	SpecimenPreparing  SpecimenStatus = "preparing"
	SpecimenStabilized SpecimenStatus = "stabilized"
	SpecimenAvailable  SpecimenStatus = "available"
)

type Specimen struct {
	ID            string         `json:"id"`
	CatalogNumber string         `json:"catalog_number"`
	FieldLabel    string         `json:"field_label"`
	TaxonHint     string         `json:"taxon_hint"`
	MatrixKind    string         `json:"matrix_kind"`
	JacketNotes   string         `json:"jacket_notes"`
	Status        SpecimenStatus `json:"status"`
	Version       int            `json:"version"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func NewSpecimen(
	id string,
	catalogNumber string,
	fieldLabel string,
	taxonHint string,
	matrixKind string,
	jacketNotes string,
	now time.Time,
) (Specimen, error) {
	if id == "" {
		return Specimen{}, contracts.Internal("missing_generated_id", "标本标识未生成", nil)
	}
	catalog, err := validateCatalogNumber(catalogNumber)
	if err != nil {
		return Specimen{}, err
	}
	field, err := requiredText("野外标签", fieldLabel, 80)
	if err != nil {
		return Specimen{}, err
	}
	taxon, err := optionalText("分类提示", taxonHint, 120)
	if err != nil {
		return Specimen{}, err
	}
	matrix, err := requiredText("基质类型", matrixKind, 60)
	if err != nil {
		return Specimen{}, err
	}
	notes, err := optionalText("夹克备注", jacketNotes, 500)
	if err != nil {
		return Specimen{}, err
	}
	return Specimen{
		ID:            id,
		CatalogNumber: catalog,
		FieldLabel:    field,
		TaxonHint:     taxon,
		MatrixKind:    matrix,
		JacketNotes:   notes,
		Status:        SpecimenRegistered,
		Version:       1,
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}, nil
}

func (s Specimen) CanCreatePlan() bool {
	return s.Status == SpecimenRegistered || s.Status == SpecimenPlanned
}

func (s Specimen) IsStable() bool {
	return s.Status == SpecimenStabilized
}

func (s *Specimen) MarkPlanned(now time.Time) error {
	if s.Status == SpecimenPlanned {
		return nil
	}
	if s.Status != SpecimenRegistered {
		return contracts.Conflict("specimen_not_plannable", "当前标本状态不能创建修复方案")
	}
	s.Status = SpecimenPlanned
	s.Version++
	s.UpdatedAt = now.UTC()
	return nil
}

func (s *Specimen) MarkPreparing(now time.Time) error {
	if s.Status == SpecimenPreparing {
		return nil
	}
	if s.Status != SpecimenPlanned {
		return contracts.Conflict("specimen_not_preparing", "只有已规划标本才能进入制备状态")
	}
	s.Status = SpecimenPreparing
	s.Version++
	s.UpdatedAt = now.UTC()
	return nil
}

func (s *Specimen) MarkStabilized(now time.Time) error {
	if s.Status == SpecimenStabilized {
		return nil
	}
	if s.Status != SpecimenPreparing {
		return contracts.Conflict("specimen_not_stabilizable", "只有正在制备的标本才能进入稳定状态")
	}
	s.Status = SpecimenStabilized
	s.Version++
	s.UpdatedAt = now.UTC()
	return nil
}

func (s *Specimen) MarkAvailable(now time.Time) error {
	if s.Status != SpecimenStabilized {
		return contracts.Conflict("specimen_not_releasable", "只有已稳定标本可以办理放行")
	}
	s.Status = SpecimenAvailable
	s.Version++
	s.UpdatedAt = now.UTC()
	return nil
}

// CustodyState describes who currently holds a specimen. A specimen that has an
// open consignment is held by an external partner instead of the station.
type CustodyState string

const (
	CustodyStation     CustodyState = "station"
	CustodyConsignment CustodyState = "consignment"
)

type ConsignmentStatus string

const (
	ConsignmentOpen   ConsignmentStatus = "open"
	ConsignmentClosed ConsignmentStatus = "closed"
)

// Consignment records a specimen handed to an external partner for work that
// cannot be performed on site. An open consignment is the authoritative record
// for where the specimen currently is.
type Consignment struct {
	ID               string            `json:"id"`
	SpecimenID       string            `json:"specimen_id"`
	RequestID        string            `json:"request_id,omitempty"`
	Vendor           string            `json:"vendor"`
	Reason           string            `json:"reason"`
	DispatchedBy     string            `json:"dispatched_by"`
	DispatchedAt     time.Time         `json:"dispatched_at"`
	ExpectedBackOn   time.Time         `json:"expected_back_on"`
	Status           ConsignmentStatus `json:"status"`
	ReturnedAt       *time.Time        `json:"returned_at,omitempty"`
	AcceptedBy       string            `json:"accepted_by,omitempty"`
	ReturnConclusion string            `json:"return_conclusion,omitempty"`
	Version          int               `json:"version"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

func NewConsignment(
	id string,
	specimen Specimen,
	requestID string,
	vendor string,
	reason string,
	dispatchedBy string,
	expectedBackOn string,
	now time.Time,
) (Consignment, error) {
	if id == "" {
		return Consignment{}, contracts.Internal("missing_generated_id", "外协标识未生成", nil)
	}
	if specimen.ID == "" {
		return Consignment{}, contracts.Internal("missing_specimen_identity", "外协缺少标本标识", nil)
	}
	if specimen.Status == SpecimenAvailable {
		return Consignment{}, contracts.Conflict("specimen_already_released", "已放行的标本不能再次委托外协")
	}
	vendorValue, err := requiredText("受托单位", vendor, 120)
	if err != nil {
		return Consignment{}, err
	}
	reasonValue, err := requiredText("外协原因", reason, 200)
	if err != nil {
		return Consignment{}, err
	}
	operator, err := requiredText("经手人", dispatchedBy, 80)
	if err != nil {
		return Consignment{}, err
	}
	deadline, err := parseConsignmentDeadline(expectedBackOn)
	if err != nil {
		return Consignment{}, err
	}
	stamp := now.UTC()
	return Consignment{
		ID:             id,
		SpecimenID:     specimen.ID,
		RequestID:      strings.TrimSpace(requestID),
		Vendor:         vendorValue,
		Reason:         reasonValue,
		DispatchedBy:   operator,
		DispatchedAt:   stamp,
		ExpectedBackOn: deadline,
		Status:         ConsignmentOpen,
		Version:        1,
		UpdatedAt:      stamp,
	}, nil
}

func (c Consignment) IsOpen() bool {
	return c.Status == ConsignmentOpen
}

func (c *Consignment) Close(acceptedBy string, conclusion string, now time.Time) error {
	if c.Status == ConsignmentClosed {
		return contracts.Conflict("consignment_already_closed", "外协委托已闭环，验收结果不能改写")
	}
	operator, err := requiredText("验收人", acceptedBy, 80)
	if err != nil {
		return err
	}
	note, err := requiredText("回厂结论", conclusion, 200)
	if err != nil {
		return err
	}
	stamp := now.UTC()
	c.Status = ConsignmentClosed
	c.ReturnedAt = &stamp
	c.AcceptedBy = operator
	c.ReturnConclusion = note
	c.Version++
	c.UpdatedAt = stamp
	return nil
}

func parseConsignmentDeadline(value string) (time.Time, error) {
	cleaned, err := requiredText("预计回厂日期", value, 10)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse("2006-01-02", cleaned)
	if err != nil {
		return time.Time{}, contracts.Invalid("invalid_expected_back_on", "预计回厂日期必须使用 YYYY-MM-DD 格式")
	}
	return parsed.UTC(), nil
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
}

type planView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Status     string `json:"status"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

type consignmentView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Status     string `json:"status"`
	AcceptedBy string `json:"accepted_by"`
}

type consignmentListView struct {
	Custody string            `json:"custody"`
	Current *consignmentView  `json:"current"`
	History []consignmentView `json:"history"`
}

type dispatchResult struct {
	status int
	err    error
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	handler := httpapi.NewRouter(station.New(repository.NewMemory()))
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			releasePayload(),
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil
	case "consignment-custody":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		consignmentsURL := server.URL + "/specimens/" + specimen.ID + "/consignments"

		results := make(chan dispatchResult, 2)
		go postDispatch(client, consignmentsURL, "dispatch-2026-a", results)
		go postDispatch(client, consignmentsURL, "dispatch-2026-b", results)
		first, second := <-results, <-results
		if first.err != nil {
			return first.err
		}
		if second.err != nil {
			return second.err
		}
		created, conflicted := 0, 0
		for _, result := range []dispatchResult{first, second} {
			switch result.status {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				conflicted++
			}
		}
		if created != 1 || conflicted != 1 {
			return fmt.Errorf("concurrent dispatch produced %d created and %d conflicted", created, conflicted)
		}
		view, err := fetchConsignmentView(client, consignmentsURL)
		if err != nil {
			return err
		}
		if view.Custody != "consignment" || view.Current == nil || len(view.History) != 1 {
			return errors.New("expected exactly one open consignment after concurrent dispatch")
		}
		open := *view.Current

		if err := requestJSON(client, http.MethodPost, consignmentsURL, dispatchPayload("dispatch-2026-c"), http.StatusConflict, nil); err != nil {
			return err
		}
		if err := requestJSON(client, http.MethodPost, server.URL+"/specimens/"+specimen.ID+"/release", releasePayload(), http.StatusConflict, nil); err != nil {
			return err
		}

		other, err := registerSpecimenWithCatalog(client, server.URL, "PPS-2026-002")
		if err != nil {
			return err
		}
		misrouted := server.URL + "/specimens/" + other.ID + "/consignments/" + open.ID + "/return"
		if err := requestJSON(client, http.MethodPost, misrouted, returnPayload("keeper-zhou"), http.StatusNotFound, nil); err != nil {
			return err
		}

		var returned consignmentView
		if err := requestJSON(client, http.MethodPost, consignmentsURL+"/"+open.ID+"/return", returnPayload("keeper-zhou"), http.StatusOK, &returned); err != nil {
			return err
		}
		if returned.Status != "closed" || returned.AcceptedBy != "keeper-zhou" {
			return errors.New("consignment did not close with the recorded acceptance")
		}
		if err := requestJSON(client, http.MethodPost, consignmentsURL+"/"+open.ID+"/return", returnPayload("keeper-wang"), http.StatusConflict, nil); err != nil {
			return err
		}
		view, err = fetchConsignmentView(client, consignmentsURL)
		if err != nil {
			return err
		}
		if view.Custody != "station" || view.Current != nil || len(view.History) != 1 {
			return errors.New("custody did not return to the station after acceptance")
		}
		if view.History[0].Status != "closed" || view.History[0].AcceptedBy != "keeper-zhou" {
			return errors.New("closed consignment history was rewritten")
		}

		var secondConsignment consignmentView
		if err := requestJSON(client, http.MethodPost, consignmentsURL, dispatchPayload("dispatch-2026-d"), http.StatusCreated, &secondConsignment); err != nil {
			return err
		}
		if err := requestJSON(client, http.MethodPost, consignmentsURL+"/"+secondConsignment.ID+"/return", returnPayload("keeper-zhou"), http.StatusOK, nil); err != nil {
			return err
		}
		view, err = fetchConsignmentView(client, consignmentsURL)
		if err != nil {
			return err
		}
		if view.Custody != "station" || view.Current != nil || len(view.History) != 2 {
			return errors.New("expected two closed consignments with custody back at the station")
		}

		var release releaseView
		if err := requestJSON(client, http.MethodPost, server.URL+"/specimens/"+specimen.ID+"/release", releasePayload(), http.StatusCreated, &release); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		if err := requestJSON(client, http.MethodPost, consignmentsURL, dispatchPayload("dispatch-2026-e"), http.StatusConflict, nil); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	return registerSpecimenWithCatalog(client, baseURL, "PPS-2026-001")
}

func registerSpecimenWithCatalog(client *http.Client, baseURL, catalog string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: catalog,
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func dispatchPayload(requestID string) contracts.DispatchConsignmentRequest {
	return contracts.DispatchConsignmentRequest{
		RequestID:      requestID,
		Vendor:         "华东化石加固中心",
		Reason:         "整体加固与托板加装",
		DispatchedBy:   "preparator-chen",
		ExpectedBackOn: "2026-11-15",
	}
}

func returnPayload(acceptedBy string) contracts.ReturnConsignmentRequest {
	return contracts.ReturnConsignmentRequest{
		AcceptedBy: acceptedBy,
		Conclusion: "整体加固完成，托板加装合格",
	}
}

func releasePayload() contracts.ReleaseRequest {
	return contracts.ReleaseRequest{
		Purpose:    "比较解剖研究",
		Recipient:  "地层研究组",
		ApprovedBy: "keeper-zhou",
	}
}

func postDispatch(client *http.Client, url, requestID string, results chan<- dispatchResult) {
	status, err := postStatus(client, url, dispatchPayload(requestID))
	results <- dispatchResult{status: status, err: err}
}

func fetchConsignmentView(client *http.Client, url string) (consignmentListView, error) {
	var view consignmentListView
	err := requestJSON(client, http.MethodGet, url, nil, http.StatusOK, &view)
	return view, err
}

func postStatus(client *http.Client, url string, body any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20)); err != nil {
		return 0, err
	}
	return response.StatusCode, nil
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}

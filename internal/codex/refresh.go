package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/termsafe"
)

const refreshTimeout = 30 * time.Second

func (s *Store) ensureFresh(ctx context.Context, reason Reason) (PublicAuth, error) {
	p, err := s.readProfile()
	if err != nil {
		return PublicAuth{}, err
	}
	// Record an interrupted attempt before deciding whether another refresh is due.
	// An active attempt must finish before applying the client throttle to its old expiry.
	due := p.Attempt != nil
	var t Tokens
	if !due {
		t, err = s.tokens()
		if err != nil {
			return PublicAuth{}, err
		}
		due, err = s.refreshDue(p, t, reason)
		if err != nil {
			return PublicAuth{}, err
		}
	}
	if !due {
		return s.Public()
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return PublicAuth{}, err
	}
	defer unlock(lock)
	record, err := s.readDiskProfile()
	p = record.Profile
	if err != nil {
		return PublicAuth{}, err
	}
	if p.Attempt != nil {
		p, err = s.recoverProfile(record)
		if err != nil {
			return PublicAuth{}, err
		}
		if err := s.saveProfile(p); err != nil {
			return PublicAuth{}, err
		}
	}
	t, err = s.tokens()
	if err != nil {
		return PublicAuth{}, err
	}
	due, err = s.refreshDue(p, t, reason)
	if err != nil {
		return PublicAuth{}, err
	}
	if !due {
		return s.Public()
	}
	p.Attempt = &Attempt{Generation: swap.Fingerprint(t.RefreshToken), Started: s.cfg.Now().UTC()}
	if reason.Kind == Client {
		p.ClientRefreshAt = p.Attempt.Started
	}
	if err := s.saveProfile(p); err != nil {
		return PublicAuth{}, err
	}
	if err := s.refresh(ctx, p, t); err != nil {
		return PublicAuth{}, err
	}
	return s.Public()
}

func (s *Store) refreshDue(p Profile, t Tokens, reason Reason) (bool, error) {
	now := s.cfg.Now()
	switch reason.Kind {
	case Background:
		if p.Health == HealthDead || p.Health == HealthStuck || now.Before(p.RetryAt) {
			return false, nil
		}
		c, err := decodeClaims(t.AccessToken)
		if err != nil {
			return false, err
		}
		issued := time.Unix(c.Issued, 0)
		if c.Issued <= 0 || !issued.Before(p.Expires) {
			issued = p.LastRefresh
		}
		return !now.Before(issued.Add(p.Expires.Sub(issued) / 2)), nil
	case Client:
		rejected := reason.Rejected != "" && reason.Rejected == swap.Fingerprint(t.AccessToken)
		if !rejected && p.Expires.After(now.Add(5*time.Minute)) {
			return false, nil
		}
	case Force:
	default:
		return false, errors.New("unknown codex refresh reason")
	}
	if p.Health == HealthDead {
		return false, ErrDead
	}
	if p.Health == HealthStuck && reason.Kind != Force {
		return false, ErrStuck
	}
	if reason.Kind == Client && (now.Before(p.RetryAt) ||
		(!p.ClientRefreshAt.IsZero() && now.Before(p.ClientRefreshAt.Add(10*time.Minute)))) {
		return false, ErrUnavailable
	}
	return true, nil
}

func (s *Store) refresh(ctx context.Context, p Profile, old Tokens) error {
	body, _ := json.Marshal(struct {
		ClientID     string `json:"client_id"`
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}{ClientID, "refresh_token", old.RefreshToken})
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		// A write error can follow a partial request. Treat even that as possibly consumed.
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, bytes.NewReader(body))
	if err != nil {
		return s.backoff(p, "")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.cfg.Client.Do(request)
	if err != nil {
		if wrote.Load() {
			return s.stuck(p, "the token endpoint did not answer: run hib connect codex")
		}
		return s.backoff(p, "")
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if len(data) > 1<<20 {
		readErr = errors.New("token answer is too large")
		data = nil
	}
	code := errorCode(data)
	if response.StatusCode == http.StatusUnauthorized ||
		(response.StatusCode == http.StatusBadRequest && deadCode(code)) {
		return s.dead(p, code)
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		return s.backoff(p, response.Header.Get("Retry-After"))
	}
	if readErr != nil || response.StatusCode != http.StatusOK {
		return s.stuck(p, "the token endpoint returned an unknown outcome: run hib connect codex")
	}
	var answer struct {
		AccessToken  string  `json:"access_token"`
		IDToken      *string `json:"id_token"`
		RefreshToken *string `json:"refresh_token"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.AccessToken == "" {
		return s.stuck(p, "the token endpoint returned an invalid answer: run hib connect codex")
	}
	next := old
	next.AccessToken = answer.AccessToken
	if answer.IDToken != nil {
		next.IDToken = *answer.IDToken
	}
	if answer.RefreshToken != nil {
		next.RefreshToken = *answer.RefreshToken
	}
	updated, err := tokenProfile(next)
	if err != nil || updated.AccountID != p.AccountID {
		return s.stuck(p, "the token endpoint returned a different account or invalid tokens: run hib connect codex")
	}
	// Once an answer has arrived, cancellation must not leave rotated credentials unpublished.
	publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer publishCancel()
	if err := publishCtx.Err(); err != nil {
		return ErrStuck
	}
	now := s.cfg.Now().UTC()
	changed, err := s.publishTokens(publishCtx, next, now)
	if err != nil {
		s.cfg.Logf("codex refresh could not be published: run hib connect codex")
		return errors.Join(ErrStuck, err)
	}
	p.Email, p.Plan, p.Expires, p.LastRefresh = updated.Email, updated.Plan, updated.Expires, now
	if changed {
		p.Published = now
	}
	p.Health, p.Problem, p.Attempt = HealthOK, "", nil
	p.Failures, p.RetryAt = 0, time.Time{}
	if err := s.saveProfile(p); err != nil {
		return errors.Join(ErrStuck, err)
	}
	s.cfg.Logf("codex refreshed")
	return nil
}

func errorCode(data []byte) string {
	var answer struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(data, &answer) != nil {
		return ""
	}
	var code string
	if json.Unmarshal(answer.Error, &code) == nil {
		return strings.ToLower(code)
	}
	var nested struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(answer.Error, &nested) == nil && nested.Code != "" {
		return strings.ToLower(nested.Code)
	}
	return strings.ToLower(answer.Code)
}

func deadCode(code string) bool {
	switch code {
	case "invalid_grant", "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
		return true
	}
	return false
}

func (s *Store) backoff(p Profile, retryAfter string) error {
	now := s.cfg.Now().UTC()
	p.Failures++
	delay := min(time.Minute*time.Duration(1<<min(p.Failures-1, 6)), time.Hour)
	if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && seconds > 0 {
		delay = time.Duration(min(seconds, 3600)) * time.Second
	} else if until, err := http.ParseTime(retryAfter); err == nil && until.After(now) {
		delay = min(until.Sub(now), time.Hour)
	}
	p.Attempt, p.RetryAt = nil, now.Add(delay)
	if err := s.saveProfile(p); err != nil {
		return err
	}
	s.cfg.Logf("codex refresh backing off until %s", p.RetryAt.Format(time.RFC3339))
	return ErrUnavailable
}

func (s *Store) dead(p Profile, code string) error {
	why := "the token endpoint refused the refresh token"
	// Only known codes are diagnostic-safe. An unknown code could echo a credential.
	if deadCode(code) {
		why = termsafe.String(code)
	}
	p.Health, p.Problem = HealthDead, why+": run hib connect codex"
	p.Attempt, p.RetryAt = nil, time.Time{}
	if err := s.saveProfile(p); err != nil {
		return err
	}
	s.cfg.Logf("codex login is dead: %s", why)
	return ErrDead
}

func (s *Store) stuck(p Profile, problem string) error {
	p.Health, p.Problem, p.RetryAt = HealthStuck, problem, time.Time{}
	if err := s.saveProfile(p); err != nil {
		return err
	}
	s.cfg.Logf("codex refresh is stuck: %s", problem)
	return ErrStuck
}

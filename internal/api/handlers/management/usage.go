package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

type usageClaimRecord struct {
	DeliveryID string           `json:"delivery_id"`
	Payload    usageQueueRecord `json:"payload"`
}

type usageClaimRequest struct {
	Count        int `json:"count"`
	LeaseSeconds int `json:"lease_seconds"`
}

type usageAckRequest struct {
	LeaseID     string   `json:"lease_id"`
	DeliveryIDs []string `json:"delivery_ids"`
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

// ClaimUsageQueue leases usage records without deleting them.
func (h *Handler) ClaimUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	request := usageClaimRequest{Count: 100, LeaseSeconds: 120}
	if errBind := c.ShouldBindJSON(&request); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid claim request"})
		return
	}
	if request.Count <= 0 || request.Count > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "count must be between 1 and 1000"})
		return
	}
	if request.LeaseSeconds <= 0 || request.LeaseSeconds > 3600 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lease_seconds must be between 1 and 3600"})
		return
	}
	claim, errClaim := redisqueue.Claim(request.Count, time.Duration(request.LeaseSeconds)*time.Second)
	if errClaim != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errClaim.Error()})
		return
	}
	items := make([]usageClaimRecord, 0, len(claim.Items))
	for _, item := range claim.Items {
		items = append(items, usageClaimRecord{DeliveryID: item.DeliveryID, Payload: usageQueueRecord(item.Payload)})
	}
	c.JSON(http.StatusOK, gin.H{"lease_id": claim.LeaseID, "items": items})
}

// AckUsageQueue deletes only committed records from the matching lease.
func (h *Handler) AckUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	var request usageAckRequest
	if errBind := c.ShouldBindJSON(&request); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ack request"})
		return
	}
	if strings.TrimSpace(request.LeaseID) == "" || len(request.DeliveryIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lease_id and delivery_ids are required"})
		return
	}
	acked, errAck := redisqueue.Ack(request.LeaseID, request.DeliveryIDs)
	if errAck != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errAck.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"acked": acked})
}

// GetUsageQueueStatus returns delivery health and aggregate counters.
func (h *Handler) GetUsageQueueStatus(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	c.JSON(http.StatusOK, redisqueue.Status())
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

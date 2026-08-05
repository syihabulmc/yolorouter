package contentinspect

import "testing"

func TestIsContentInspectionRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "vendor code data_inspection_failed",
			status: 400,
			body:   `{"error":{"message":"Input data may contain inappropriate content.","type":"invalid_request_error","code":"data_inspection_failed"}}`,
			want:   true,
		},
		{
			name:   "camelcase spelling of the same code",
			status: 400,
			body:   `{"code":"DataInspectionFailed","message":"Input data may contain inappropriate content."}`,
			want:   true,
		},
		{
			name:   "content_filter with nested filter result",
			status: 400,
			body:   `{"error":{"code":"content_filter","innererror":{"content_filter_result":{"hate":{"filtered":true}}}}}`,
			want:   true,
		},
		{
			name:   "content_policy_violation",
			status: 400,
			body:   `{"error":{"code":"content_policy_violation","message":"Your request was rejected as a result of our safety system."}}`,
			want:   true,
		},
		{
			name:   "sensitive content detected",
			status: 400,
			body:   `{"error":{"code":"SensitiveContentDetected","message":"the request was rejected"}}`,
			want:   true,
		},
		{
			name:   "prose-only refusal with an opaque numeric code",
			status: 400,
			body:   `{"error":{"code":"1301","message":"系统检测到输入或生成内容可能包含不安全或敏感内容"}}`,
			want:   true,
		},
		{
			name:   "content exists risk",
			status: 400,
			body:   `{"error":{"message":"Content Exists Risk","type":"invalid_request_error"}}`,
			want:   true,
		},
		{
			name:   "451 with moderation prose",
			status: 451,
			body:   `{"message":"prompt blocked by moderation"}`,
			want:   true,
		},

		// Ordinary request-shape 4xx: identical at every candidate, so they
		// must keep failing fast instead of walking the chain.
		{
			name:   "schema violation is not moderation",
			status: 400,
			body:   `{"error":{"message":"input violates the schema: messages[0].role must be one of user, assistant"}}`,
			want:   false,
		},
		{
			name:   "unknown parameter",
			status: 400,
			body:   `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`,
			want:   false,
		},
		{
			name:   "context length exceeded",
			status: 400,
			body:   `{"error":{"message":"This model's maximum context length is 65536 tokens. However, your input resulted in 70000 tokens.","code":"context_length_exceeded"}}`,
			want:   false,
		},
		{
			name:   "unsupported content type",
			status: 400,
			body:   `{"error":{"message":"Invalid content type, expected application/json"}}`,
			want:   false,
		},
		{
			name:   "auth failure",
			status: 401,
			body:   `{"error":{"message":"Incorrect API key provided","code":"invalid_api_key"}}`,
			want:   false,
		},

		// Status gating.
		{
			name:   "moderation prose on a 500 is an outage, not a refusal",
			status: 500,
			body:   `{"error":{"message":"Input data may contain inappropriate content."}}`,
			want:   false,
		},
		{
			name:   "moderation code on a 404",
			status: 404,
			body:   `{"error":{"code":"data_inspection_failed"}}`,
			want:   false,
		},
		{
			name:   "empty body",
			status: 400,
			body:   "",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRefusal(tc.status, tc.body); got != tc.want {
				t.Fatalf("IsRefusal(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

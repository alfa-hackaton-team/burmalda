// Public API v0.1 DTOs. Source of truth: docs/api/{openapi,schemas}.yaml.
export type Context = { context_id: string } & (
  { kind: 'COMPANY'; company_id: string } | { kind: 'GROUP'; group_id: string }
);
export type ProfileRef = { profile_id: string; version: string };
export type AvailableContext = {
  context: Context;
  display_name: string;
  role_id: string;
  profile?: ProfileRef;
};
export type Page<T> = { items: T[]; next_cursor: string | null };
export type User = { user_id: string; display_name?: string };
export type EventRef = { event_id: string; title: string; summary: string };
export type FeatureRef = {
  profile: ProfileRef;
  feature_id: string;
  contributing_company_ids: string[];
};
export type SourceRef = {
  source_id: string;
  name: string;
  source_role: 'external' | 'bank' | 'expert';
  url?: string;
  published_at?: string | null;
};
export type FeedItem = {
  preview_id: string;
  event: EventRef;
  published_at: string;
  topic: string;
  why_relevant: string;
  feature_refs: FeatureRef[];
  sources: SourceRef[];
  impact: { label: string; explanation: string; policy_version: string };
};
export type Evidence =
  | {
      evidence_id: string;
      evidence_type: 'EXTERNAL_NEWS' | 'ALFA_ANALYTICS';
      reference: { source_id: string; document_version_id: string; chunk_id: string };
      excerpt: string;
      published_at: string | null;
      fetched_at: string;
    }
  | {
      evidence_id: string;
      evidence_type: 'COMPANY_DATA';
      reference: FeatureRef;
      content: string;
      provenance: { source_snapshot_id: string; record_refs: string[]; derivation_version: string };
    };
export type Claim = {
  claim_id: string;
  text: string;
  claim_type: 'FACT' | 'ALFA_ANALYTICS' | 'AI_INFERENCE' | 'FORECAST';
  verification_status: 'SUPPORTED' | 'PARTIALLY_SUPPORTED' | 'UNSUPPORTED';
  evidence_links: { evidence_id: string; relation: 'SUPPORT' | 'CONTRADICT' | 'CONTEXT' }[];
  assumptions: string[];
  time_horizon?: string;
};
export type SectionType =
  | 'WHAT_HAPPENED'
  | 'WHY_RELEVANT'
  | 'RISKS'
  | 'OPPORTUNITIES'
  | 'SCENARIOS_ACTIONS'
  | 'BANK_VIEW'
  | 'AI_ANALYSIS';
export type AgentResponse = {
  trace_id: string;
  claims: Claim[];
  evidence: Evidence[];
  sections: { section_type: SectionType; claim_ids: string[] }[];
  limitations: { code: string; message: string; related_claim_ids?: string[] }[];
  model_config_ref: string;
};
export type Insight = {
  insight_id: string;
  revision: string;
  primary_event: EventRef;
  supporting_event_ids: string[];
  context: Context;
  role_id: string;
  profile: ProfileRef;
  created_at: string;
  response: AgentResponse;
};
export type ApiErrorData = { code: string; message: string; retryable: boolean; trace_id?: string };
export type Generation = {
  generation_id: string;
  state: 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'INSUFFICIENT_EVIDENCE' | 'FAILED';
  trace_id: string;
  created_at: string;
  insight_id?: string;
  message_id?: string;
  error?: ApiErrorData;
};
export type Conversation = {
  conversation_id: string;
  insight_id: string;
  insight_revision: string;
  context: Context;
  role_id: string;
  created_at: string;
};
export type Message = {
  message_id: string;
  conversation_id: string;
  sequence: number;
  author: 'USER' | 'ASSISTANT';
  created_at: string;
  text?: string;
  response?: AgentResponse;
};
export type FollowUp = { user_message: Message; assistant_message: Message };
export type FeedbackInput = {
  insight_revision: string;
  rating: 'POSITIVE' | 'NEGATIVE';
  reason?: 'IRRELEVANT' | 'INCORRECT_CONCLUSION' | 'POOR_SOURCES' | 'TOO_GENERIC' | 'OTHER';
  comment?: string;
};
export type Feedback = {
  feedback_id: string;
  insight_id: string;
  created_at: string;
  input: FeedbackInput;
};
export type Trace = {
  trace_id: string;
  stages: {
    stage_id: string;
    stage_type: string;
    status: string;
    started_at: string;
    finished_at: string | null;
    evidence_ids: string[];
    feature_refs: FeatureRef[];
    summary?: string;
  }[];
};

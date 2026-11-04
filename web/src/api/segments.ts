import api from './client'
import type {
  ApiResponse,
  PaginatedResponse,
  Segment,
  SegmentCondition,
  SegmentInput,
  SegmentPreview,
  SegmentRow,
} from './types'

const BASE = '/workspaces/current/segments'

/**
 * Error extracted from a segments API failure. Validation 400s come back as a
 * flat body — { error: "…", validation_errors: [...] } — unlike the envelope
 * errors ({ error: { message } }), so both shapes are handled here and callers
 * surface them verbatim.
 */
export interface SegmentsApiError {
  message: string
  validationErrors: string[]
}

export function segmentsApiError(e: any, fallback = 'Request failed'): SegmentsApiError {
  const data = e?.response?.data
  const validationErrors: string[] = Array.isArray(data?.validation_errors)
    ? data.validation_errors.filter((v: unknown) => typeof v === 'string')
    : []
  let message = ''
  if (typeof data?.error === 'string') message = data.error
  else if (data?.error?.message) message = data.error.message
  else if (data?.error?.error) message = data.error.error
  else if (data?.message) message = data.message
  return { message: message || fallback, validationErrors }
}

export interface SegmentPreviewParams {
  condition: SegmentCondition
  page?: number
  size?: number
}

export const segmentsApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<SegmentRow>>(BASE, { params: { page, size } })
  },

  get(id: number) {
    return api.get<ApiResponse<Segment>>(`${BASE}/${id}`)
  },

  create(payload: SegmentInput) {
    return api.post<ApiResponse<Segment>>(BASE, payload)
  },

  update(id: number, payload: SegmentInput) {
    return api.put<ApiResponse<Segment>>(`${BASE}/${id}`, payload)
  },

  remove(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`${BASE}/${id}`)
  },

  /** Live count + paged contact sample for an (unsaved) condition. */
  preview(params: SegmentPreviewParams) {
    return api.post<ApiResponse<SegmentPreview>>(`${BASE}/preview`, {
      condition: params.condition,
      page: params.page ?? 0,
      size: params.size ?? 20,
    })
  },

  /** Paged contacts of a saved segment — same shape as preview. */
  contacts(id: number, page = 0, size = 20) {
    return api.get<ApiResponse<SegmentPreview>>(`${BASE}/${id}/contacts`, { params: { page, size } })
  },
}

// ─── Field catalog (shared by the drawer builder and the list summary) ──────

export type SegmentFieldType = 'text' | 'enum' | 'date' | 'number' | 'boolean' | 'custom'
export type SegmentFieldGroup = 'subscriber' | 'engagement' | 'custom'

export interface SegmentFieldDef {
  /** Wire field name. Custom fields are `custom_fields.<key>` at runtime. */
  field: string
  label: string
  type: SegmentFieldType
  group: SegmentFieldGroup
  operators: string[]
  /** Operator applied when the field is picked. */
  defaultOperator: string
  /** Fixed choices for enum / boolean value editors. */
  options?: { value: string; label: string }[]
}

const TEXT_OPERATORS = [
  'equals',
  'not_equals',
  'contains',
  'not_contains',
  'starts_with',
  'ends_with',
  'is_empty',
  'is_not_empty',
  'one_of',
  'not_one_of',
]

/** Operators whose value input is hidden. */
export const NO_VALUE_OPERATORS = ['is_empty', 'is_not_empty', 'exists', 'not_exists']

/** Marker option for the "Custom field…" select entry. */
export const CUSTOM_FIELD_OPTION = '__custom'
export const CUSTOM_FIELD_PREFIX = 'custom_fields.'

export const SEGMENT_FIELDS: SegmentFieldDef[] = [
  { field: 'email', label: 'Email', type: 'text', group: 'subscriber', operators: TEXT_OPERATORS, defaultOperator: 'contains' },
  { field: 'name', label: 'Name', type: 'text', group: 'subscriber', operators: TEXT_OPERATORS, defaultOperator: 'contains' },
  { field: 'timezone', label: 'Timezone', type: 'text', group: 'subscriber', operators: TEXT_OPERATORS, defaultOperator: 'equals' },
  { field: 'language', label: 'Language', type: 'text', group: 'subscriber', operators: TEXT_OPERATORS, defaultOperator: 'equals' },
  {
    field: 'status',
    label: 'Status',
    type: 'enum',
    group: 'subscriber',
    operators: TEXT_OPERATORS,
    defaultOperator: 'one_of',
    options: [
      { value: 'subscribed', label: 'Subscribed' },
      { value: 'unsubscribed', label: 'Unsubscribed' },
      { value: 'bounced', label: 'Bounced' },
      { value: 'complained', label: 'Complained' },
    ],
  },
  { field: 'created_at', label: 'Created at', type: 'date', group: 'subscriber', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'subscribed_at', label: 'Subscribed at', type: 'date', group: 'subscriber', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'unsubscribed_at', label: 'Unsubscribed at', type: 'date', group: 'subscriber', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'engagement.inactive_days', label: 'Inactive days', type: 'number', group: 'engagement', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'engagement.active_days', label: 'Active days', type: 'number', group: 'engagement', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'engagement.total_opens_gte', label: 'Total opens', type: 'number', group: 'engagement', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'engagement.total_clicks_gte', label: 'Total clicks', type: 'number', group: 'engagement', operators: ['greater_than', 'less_than'], defaultOperator: 'greater_than' },
  { field: 'engagement.opened_campaign', label: 'Opened a campaign', type: 'boolean', group: 'engagement', operators: ['equals'], defaultOperator: 'equals' },
  { field: 'engagement.clicked_campaign', label: 'Clicked a campaign', type: 'boolean', group: 'engagement', operators: ['equals'], defaultOperator: 'equals' },
  { field: 'engagement.not_opened_campaign', label: 'Not opened a campaign', type: 'boolean', group: 'engagement', operators: ['equals'], defaultOperator: 'equals' },
  { field: 'engagement.received_campaign', label: 'Received a campaign', type: 'boolean', group: 'engagement', operators: ['equals'], defaultOperator: 'equals' },
]

export const SEGMENT_CUSTOM_OPERATORS = [...TEXT_OPERATORS, 'greater_than', 'less_than', 'exists', 'not_exists']

export function isCustomField(field: string): boolean {
  return typeof field === 'string' && field.startsWith(CUSTOM_FIELD_PREFIX)
}

export function customFieldKey(field: string): string {
  return isCustomField(field) ? field.slice(CUSTOM_FIELD_PREFIX.length) : ''
}

export function customFieldDef(): SegmentFieldDef {
  return {
    field: CUSTOM_FIELD_PREFIX,
    label: 'Custom field…',
    type: 'custom',
    group: 'custom',
    operators: SEGMENT_CUSTOM_OPERATORS,
    defaultOperator: 'equals',
  }
}

/** Field def for a rule's wire field — falls back to text semantics for unknowns. */
export function fieldDefFor(field: string): SegmentFieldDef {
  if (isCustomField(field)) return customFieldDef()
  return SEGMENT_FIELDS.find((f) => f.field === field) ?? {
    field,
    label: field,
    type: 'text',
    group: 'subscriber',
    operators: TEXT_OPERATORS,
    defaultOperator: 'equals',
  }
}

/** Human phrasing used by the Conditions column summary. */
export const SEGMENT_OPERATOR_PHRASES: Record<string, string> = {
  equals: 'is',
  not_equals: 'is not',
  contains: 'contains',
  not_contains: 'does not contain',
  starts_with: 'starts with',
  ends_with: 'ends with',
  is_empty: 'is empty',
  is_not_empty: 'is not empty',
  one_of: 'is one of',
  not_one_of: 'is not one of',
  greater_than: 'is greater than',
  less_than: 'is less than',
  exists: 'exists',
  not_exists: 'does not exist',
}

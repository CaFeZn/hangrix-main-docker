export type QuestionnaireStatus = 'open' | 'closed'

export type QuestionType = 'single_choice' | 'multi_choice' | 'text_input'

/** Reserved option ID for the platform-injected "其他…" virtual option. */
export const OTHER_OPTION_ID = '__other__'

export interface Option {
  id: string
  label: string
}

export interface Question {
  id: number
  position: number
  type: QuestionType
  text: string
  required: boolean
  options: Option[] | null // null for text_input
}

export interface Questionnaire {
  id: number
  issue_id: number
  title: string
  description: string
  status: QuestionnaireStatus
  created_by_agent: string
  created_at: string
  closed_at: string | null
  closed_reason?: string
  questions: Question[]
  my_submission?: MySubmission | null
}

export interface AnswerEntry {
  question_id: number
  option_ids?: string[] // for choice types
  text?: string // for text_input
}

export interface MySubmission {
  submitted_at: string
  answers: AnswerEntry[]
}

export interface ChoiceTally {
  option_id: string
  label: string
  count: number
  percent: number
  is_other?: boolean
}

export interface TextResponse {
  user_id: number
  user_display: string
  text: string
  submitted_at: string
}

export interface QuestionResult {
  type: QuestionType
  tallies?: ChoiceTally[]
  other_responses?: TextResponse[]
  responses?: TextResponse[]
}

export interface Submitter {
  user_id: number
  user_display: string
  submitted_at: string
  answers: AnswerEntry[]
}

export interface QuestionnaireResult {
  questionnaire: Questionnaire
  submissions: number
  by_question: Record<string, QuestionResult>
  submitters: Submitter[]
}

export interface QuestionnaireListResponse {
  data: Questionnaire[]
}

export interface QuestionnaireSingleResponse {
  data: Questionnaire
}

export interface SubmitAnswersRequest {
  answers: AnswerEntry[]
}

export interface SubmitAnswersResponse {
  data: {
    answer_id: number
    submitted_at: string
    questionnaire_status: QuestionnaireStatus
  }
}

export interface QuestionnaireResultResponse {
  data: QuestionnaireResult
}

/** Server validation error codes the frontend can translate. */
export type QuestionnaireErrorCode =
  | 'missing_required_question'
  | 'unknown_option'
  | 'single_choice_multi_select'
  | 'choice_for_text_question'
  | 'text_for_choice_question'
  | 'other_text_required'
  | 'already_submitted'
  | 'questionnaire_closed'
  | 'questionnaire_locked'

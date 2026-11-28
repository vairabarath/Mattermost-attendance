// The panel's calls to the bot (through the plugin server). Types mirror
// the bot's JSON (internal/attendance/api.go).

import {doFetch} from 'client';

export type TeamRef = {id: string; name: string};

export type SessionState = {
    team: TeamRef;
    check_in: string;
    mode: 'office' | 'wfh' | string;
    on_break: boolean;
    break_since?: string;
    net_minutes: number;
    net_seconds: number;
};

export type EODBrief = {available: boolean; day?: string; day_label?: string; submitted: boolean; sync_line?: string};

export type Me = {
    now: string;
    user_id: string;
    team: TeamRef;
    is_admin: boolean;
    github_linked: boolean;
    github_login?: string;
    github_set_up: boolean;
    display_name: string;
    eod: EODBrief;
    session: SessionState | null;
    today_team_minutes: number;
    today_all_minutes: number;
    today_team_seconds: number;
    today_all_seconds: number;
};

export type SessionAction = 'in' | 'break' | 'back' | 'out';

export type ActResult = {message: string; me: Me};

const teamQuery = (teamId: string) => `?team=${encodeURIComponent(teamId)}`;

export const fetchMe = (teamId: string) => doFetch<Me>(`/me${teamQuery(teamId)}`);

export const act = (teamId: string, action: SessionAction, text = '') =>
    doFetch<ActResult>(`/session/${action}${teamQuery(teamId)}`, {method: 'post', body: JSON.stringify({text})});

export type TaskItem = {
    number: number;
    title: string;
    url: string;
    repo: string;
    status: string;
    priority?: string;
    due?: string;
    overdue: boolean;
    off_board: boolean;
    picked: boolean;
};

export type TasksState = {
    configured: boolean;
    linked: boolean;
    error?: string;
    tasks: TaskItem[];
};

export const fetchTasks = (teamId: string) => doFetch<TasksState>(`/tasks${teamQuery(teamId)}`);

export const pickTask = (teamId: string, issueURL: string) =>
    doFetch<{message: string; tasks: TasksState}>(`/tasks/pick${teamQuery(teamId)}`, {method: 'post', body: JSON.stringify({issue_url: issueURL})});

export type EODTaskRow = {number: number; title: string; url: string; repo: string; status: string; choice: string};
export type EODTaskRef = {number: number; title: string; url: string; repo: string; status?: string; checked?: boolean};

export type EODForm = {
    available: boolean;
    message?: string;
    team: TeamRef;
    day: string;
    day_label: string;
    note?: string;
    omitted?: number;
    statuses: string[];
    no_change: string;
    tasks: EODTaskRow[];
    done: EODTaskRef[];
    others: EODTaskRef[];
    did: string;
    blockers: string;
    tomorrow: string;
    min_did: number;
    max_text: number;
    submitted: boolean;
    submitted_at?: string;
    sync_line?: string;
    state: string;
};

export type EODAnswers = {
    choices: Record<string, string>;
    others: string[];
    did: string;
    blockers: string;
    tomorrow: string;
};

// FormError carries the bot's per-field messages (did, s0, others,
// start_date, reason…).
export class FormError extends Error {
    fields: Record<string, string>;
    constructor(message: string, fields: Record<string, string>) {
        super(message);
        this.fields = fields;
    }
}

export const fetchEOD = (teamId: string) => doFetch<EODForm>(`/eod${teamQuery(teamId)}`);

export async function submitEOD(teamId: string, state: string, a: EODAnswers): Promise<EODForm> {
    try {
        const r = await doFetch<{eod: EODForm}>(`/eod${teamQuery(teamId)}`, {method: 'post', body: JSON.stringify({state, ...a})});
        return r.eod;
    } catch (e) {
        const err = e as Error & {body?: {errors?: Record<string, string>}};
        if (err.body?.errors) {
            throw new FormError(err.message, err.body.errors);
        }
        throw e;
    }
}

// postForm sends a form and turns the bot's field errors into a FormError.
async function postForm<T>(path: string, body: unknown): Promise<T> {
    try {
        return await doFetch<T>(path, {method: 'post', body: JSON.stringify(body)});
    } catch (e) {
        const err = e as Error & {body?: {errors?: Record<string, string>}};
        if (err.body?.errors) {
            throw new FormError(err.message, err.body.errors);
        }
        throw e;
    }
}

// --- Hours ---

export type HoursRange = 'today' | 'week' | 'month';
export type HoursDay = {day: string; label: string; seconds: number; modes: string[]; open: boolean};
export type Hours = {
    range: HoursRange;
    label: string;
    days: HoursDay[];
    team_seconds: number;
    all_seconds: number;
    by_org: Array<{team: TeamRef; seconds: number}>;
    has_expected: boolean;
    expected_seconds: number;
    expected_until?: string;
    worked_until_yesterday: number;
    per_day_hours: number;
};

export const fetchHours = (teamId: string, range: HoursRange) =>
    doFetch<Hours>(`/hours${teamQuery(teamId)}&range=${range}`);

// --- Work from home ---

export type WFHItem = {
    id: number;
    dates: string;
    start: string;
    end: string;
    half_day: boolean;
    reason: string;
    status: 'pending' | 'approved' | 'rejected' | 'cancelled' | string;
    decided_by?: string;
    note?: string;
    can_cancel: boolean;
};
export type WFHState = {team: TeamRef; today: string; pending: number; requests: WFHItem[]};
export type WFHRequestForm = {start_date: string; end_date: string; half_day: boolean; reason: string};

export const fetchWFH = (teamId: string) => doFetch<WFHState>(`/wfh${teamQuery(teamId)}`);

export const requestWFH = async (teamId: string, f: WFHRequestForm) =>
    (await postForm<{wfh: WFHState}>(`/wfh${teamQuery(teamId)}`, f)).wfh;

export const cancelWFH = (teamId: string, id: number) =>
    doFetch<{message: string; wfh: WFHState}>(`/wfh/${id}/cancel${teamQuery(teamId)}`, {method: 'post'});

// --- My settings ---

export type Settings = {
    team: TeamRef;
    is_admin: boolean;
    github_set_up: boolean;
    github_org?: string;
    github_login?: string;
    expected_hours: number;
    tracked: boolean;
    reminders_on: boolean;
};

export const fetchSettings = (teamId: string) => doFetch<Settings>(`/settings${teamQuery(teamId)}`);

// --- Admin (team admins only; the bot checks) ---

export type TeamMemberDay = {
    user_id: string;
    username: string;
    display_name?: string;
    state: 'working' | 'break' | 'out' | 'absent' | 'elsewhere' | string;
    elsewhere?: string;
    since?: string;
    mode?: string;
    seconds: number;
    eod: 'sent' | 'missing' | 'waiting' | 'none' | string;
    done: number;
    blocked: number;
    sync?: string;
    tracked: boolean;
};

export type TeamDay = {
    team: TeamRef;
    day: string;
    day_label: string;
    members: TeamMemberDay[];
    working: number;
    on_break: number;
    out: number;
    absent: number;
    eod_sent: number;
    eod_due: number;
    wfh_pending: number;
};

export type MemberDetail = {
    user_id: string;
    username: string;
    day: string;
    day_label: string;
    worked: boolean;
    report?: string;
    submitted_at?: string;
    sync_line?: string;
    tasks?: TasksState;
};

export type AdminWFHItem = {
    id: number;
    username: string;
    dates: string;
    half_day: boolean;
    reason: string;
    status: string;
    decided_by?: string;
    note?: string;
    urgent: boolean;
    used_month?: string;
};
export type AdminWFH = {pending: AdminWFHItem[]; recent: AdminWFHItem[]; today: string[]};

export type TeamReportRow = {
    username: string;
    days: number;
    team_seconds: number;
    all_seconds: number;
    expected_seconds: number;
    has_expected: boolean;
    short: boolean;
    wfh_days: number;
    auto_closed: number;
};
export type TeamReport = {range: 'week' | 'month'; label: string; rows: TeamReportRow[]};

export const fetchTeamDay = (teamId: string) => doFetch<TeamDay>(`/admin/today${teamQuery(teamId)}`);
export const fetchMember = (teamId: string, userId: string) =>
    doFetch<MemberDetail>(`/admin/member${teamQuery(teamId)}&user=${encodeURIComponent(userId)}`);
export const fetchAdminWFH = (teamId: string) => doFetch<AdminWFH>(`/admin/wfh${teamQuery(teamId)}`);
export const decideWFH = async (teamId: string, id: number, approve: boolean, note = '') =>
    (await postForm<{wfh: AdminWFH}>(`/admin/wfh/${id}/decide${teamQuery(teamId)}`, {approve, note})).wfh;
export const fetchTeamReport = (teamId: string, range: 'week' | 'month') =>
    doFetch<TeamReport>(`/admin/report${teamQuery(teamId)}&range=${range}`);
export const exportCSV = (teamId: string, month = '') =>
    doFetch<{message: string}>(`/admin/export${teamQuery(teamId)}`, {method: 'post', body: JSON.stringify({month})});

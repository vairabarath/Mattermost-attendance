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

export type Me = {
    now: string;
    user_id: string;
    team: TeamRef;
    is_admin: boolean;
    github_linked: boolean;
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

// EODSubmitError carries the bot's per-field messages (did, s0, others…).
export class EODSubmitError extends Error {
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
            throw new EODSubmitError(err.message, err.body.errors);
        }
        throw e;
    }
}

import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import React from 'react';

import TeamPage from './TeamPage';

jest.mock('api', () => ({
    ...jest.requireActual('api'),
    fetchTeamDay: jest.fn(),
    fetchMember: jest.fn(),
    fetchAdminWFH: jest.fn(),
    decideWFH: jest.fn(),
    fetchTeamReport: jest.fn(),
    exportCSV: jest.fn(),
}));
const mocked = api as jest.Mocked<typeof api>;
const TEAM = 'tttttttttttttttttttttttttt';

const member = (u: string, over: Partial<api.TeamMemberDay> = {}): api.TeamMemberDay => ({
    user_id: `${u}id`, username: u, state: 'out', seconds: 0, eod: 'none', done: 0, blocked: 0, tracked: true, ...over,
});

const day = (): api.TeamDay => ({
    team: {id: TEAM, name: 'Bittrix'},
    day: '2026-11-27',
    day_label: 'Fri 27 Nov',
    members: [
        member('barath', {state: 'working', since: '2026-11-27T04:30:00Z', mode: 'wfh', seconds: 28800, eod: 'waiting'}),
        member('jana', {state: 'out', seconds: 28800, eod: 'sent', done: 1, blocked: 1}),
        member('ravi', {state: 'out', seconds: 14400, eod: 'missing'}),
        member('kumar', {state: 'absent'}),
    ],
    working: 1,
    on_break: 0,
    out: 2,
    absent: 1,
    eod_sent: 1,
    eod_due: 2,
    wfh_pending: 1,
});

const wfhItem = (id: number, over: Partial<api.AdminWFHItem> = {}): api.AdminWFHItem => ({
    id, username: 'ravi', dates: 'Tue 1 Dec (full day)', half_day: false, reason: 'Plumber', status: 'pending', urgent: false, ...over,
});

beforeEach(() => {
    mocked.fetchTeamDay.mockResolvedValue(day());
    mocked.fetchAdminWFH.mockResolvedValue({pending: [wfhItem(5, {urgent: true, used_month: '1'})], recent: [wfhItem(3, {status: 'approved', decided_by: 'barath', username: 'kumar'})], today: ['@kumar']});
    mocked.fetchTeamReport.mockResolvedValue({range: 'week',
        label: 'Mon 23 Nov – Fri 27 Nov',
        rows: [
            {username: 'jana', days: 3, team_seconds: 86400, all_seconds: 90000, expected_seconds: 115200, has_expected: true, short: true, wfh_days: 1, auto_closed: 0, eod_sent: 2, eod_due: 3},
            {username: 'ghost', days: 0, team_seconds: 0, all_seconds: 0, expected_seconds: 0, has_expected: false, short: false, wfh_days: 0, auto_closed: 0, eod_sent: 0, eod_due: 0},
        ],
        eod_sent: 2,
        eod_due: 3});
});

test('today: summary and one row per member with status, hours, EOD and blockers', async () => {
    render(<TeamPage teamId={TEAM}/>);
    const stats = await screen.findByLabelText('Team summary');
    expect(stats).toHaveTextContent('1 working');
    expect(stats).toHaveTextContent('1/2 EOD');
    const rows = within(screen.getByRole('list', {name: 'Team · Fri 27 Nov'})).getAllByRole('button');
    expect(rows).toHaveLength(4);
    expect(rows[0]).toHaveTextContent('@barath');
    expect(rows[0]).toHaveTextContent('🏠');
    expect(rows[0]).toHaveTextContent('8h 0m');
    expect(rows[0]).toHaveTextContent('EOD later');
    expect(rows[1]).toHaveTextContent('EOD ✓');
    expect(rows[1]).toHaveTextContent('🚧 1');
    expect(rows[2]).toHaveTextContent('No EOD');
    expect(rows[3]).toHaveTextContent('Not checked in');
    expect(screen.getByRole('tab', {name: 'WFH · 1'})).toBeInTheDocument();
});

test('today: a member opens their report and tasks; ← Team goes back', async () => {
    mocked.fetchMember.mockResolvedValue({
        user_id: 'janaid',
        username: 'jana',
        day: '2026-11-27',
        day_label: 'Fri 27 Nov',
        worked: true,
        report: '📝 **jana** — 8h 0m\n**Did:** Shipped the export',
        submitted_at: '2026-11-27T12:00:00Z',
        sync_line: 'GitHub: ✅ synced',
        tasks: {configured: true, linked: true, tasks: [{number: 7, title: 'Domain renewal', url: 'u7', repo: 'o/ops', status: 'Blocked', overdue: true, off_board: false, picked: true}]},
    });
    render(<TeamPage teamId={TEAM}/>);
    await userEvent.click(await screen.findByRole('button', {name: /^@jana/}));
    expect(mocked.fetchMember).toHaveBeenCalledWith(TEAM, 'janaid');
    expect(await screen.findByText(/Shipped the export/)).toBeInTheDocument();
    expect(screen.getByText(/GitHub: ✅ synced/)).toBeInTheDocument();
    expect(screen.getByRole('link', {name: 'Domain renewal'})).toBeInTheDocument();
    await userEvent.click(screen.getByText('← Team'));
    expect(await screen.findByLabelText('Team summary')).toBeInTheDocument();
});

test('wfh: approve; reject needs a reason (the bot’s message is shown)', async () => {
    render(<TeamPage teamId={TEAM}/>);
    await userEvent.click(await screen.findByRole('tab', {name: /^WFH/}));
    expect(await screen.findByText('Urgent')).toBeInTheDocument();
    expect(screen.getByText('🏠 WFH today: @kumar')).toBeInTheDocument();
    expect(screen.getByText('WFH days approved this month: 1')).toBeInTheDocument();
    expect(screen.getByLabelText('Last 30 days')).toHaveTextContent('@kumar · Tue 1 Dec (full day) · approved by @barath');

    mocked.decideWFH.mockRejectedValueOnce(new api.FormError('x', {reason: 'Please give a reason (it’s sent to the member).'}));
    await userEvent.click(screen.getByText('❌ Reject…'));
    await userEvent.click(screen.getByText('Reject'));
    expect(mocked.decideWFH).toHaveBeenCalledWith(TEAM, 5, false, '');
    expect(await screen.findByText(/Please give a reason/)).toBeInTheDocument();

    mocked.decideWFH.mockResolvedValueOnce({pending: [], recent: [], today: []});
    await userEvent.click(screen.getByText('Cancel'));
    await userEvent.click(screen.getByText('✅ Approve'));
    expect(mocked.decideWFH).toHaveBeenLastCalledWith(TEAM, 5, true, '');
    expect(await screen.findByText('Nothing to decide. 🎉')).toBeInTheDocument();
    expect(screen.getByRole('tab', {name: 'WFH'})).toBeInTheDocument();
});

test('reports: week / month table, ⚠️ short, CSV export to DMs', async () => {
    mocked.exportCSV.mockResolvedValue({message: '📎 Attendance export for **Bittrix**, November 2026: 12 session(s).'});
    render(<TeamPage teamId={TEAM}/>);
    await userEvent.click(await screen.findByRole('tab', {name: 'Reports'}));
    const rows = await screen.findAllByRole('row');
    expect(rows[1]).toHaveTextContent('@jana ⚠️');
    expect(rows[1]).toHaveTextContent('24h 0m');
    expect(rows[1]).toHaveTextContent('2/3');
    expect(rows[2]).toHaveTextContent('—');
    expect(screen.getByText('67%')).toBeInTheDocument();
    expect(screen.getByText('end-of-day reports sent · 2 of 3 days worked')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('tab', {name: 'This month'}));
    expect(mocked.fetchTeamReport).toHaveBeenLastCalledWith(TEAM, 'month');
    await userEvent.click(screen.getByText('⬇ CSV to my DMs'));
    expect(await screen.findByRole('status')).toHaveTextContent('Attendance export for Bittrix');
});

test('a member still checked in: the report "opens when they check out", not "missing"', async () => {
    mocked.fetchMember.mockResolvedValue({user_id: 'barathid', username: 'barath', day: '2026-11-27', day_label: 'Fri 27 Nov', worked: true});
    render(<TeamPage teamId={TEAM}/>);
    await userEvent.click(await screen.findByRole('button', {name: /^@barath/}));
    expect(await screen.findByText('Still checked in: the report opens when they check out.')).toBeInTheDocument();
    expect(screen.queryByText(/hasn’t sent/)).not.toBeInTheDocument();
});

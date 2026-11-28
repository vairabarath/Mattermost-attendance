import {act, render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import * as client from 'client';
import React from 'react';

import HoursPage from './HoursPage';
import SettingsPage from './SettingsPage';
import {tipPosition} from './WeekChart';
import WFHPage from './WFHPage';

jest.mock('api', () => ({
    ...jest.requireActual('api'),
    fetchHours: jest.fn(),
    fetchWFH: jest.fn(),
    requestWFH: jest.fn(),
    cancelWFH: jest.fn(),
    fetchSettings: jest.fn(),
}));
jest.mock('client', () => ({...jest.requireActual('client'), fetchHealth: jest.fn()}));
const mocked = api as jest.Mocked<typeof api>;
const mockedClient = client as jest.Mocked<typeof client>;
const TEAM = 'tttttttttttttttttttttttttt';

const hours = (over: Partial<api.Hours> = {}): api.Hours => ({
    range: 'week',
    label: 'Mon 23 Nov – Fri 27 Nov',
    days: [
        {day: '2026-11-23', label: 'Mon 23', seconds: 28800, modes: ['office'], open: false, workday: true},
        {day: '2026-11-24', label: 'Tue 24', seconds: 0, modes: [], open: false, workday: true},
        {day: '2026-11-25', label: 'Wed 25', seconds: 25200, modes: ['wfh'], open: false, workday: true},
        {day: '2026-11-26', label: 'Thu 26', seconds: 0, modes: [], open: false, workday: false, holiday: 'Festival'},
        {day: '2026-11-27', label: 'Fri 27', seconds: 21600, modes: ['office'], open: true, workday: true},
        {day: '2026-11-28', label: 'Sat 28', seconds: 0, modes: [], open: false, workday: false, future: true},
        {day: '2026-11-29', label: 'Sun 29', seconds: 0, modes: [], open: false, workday: false, future: true},
    ],
    team_seconds: 75600,
    all_seconds: 82800,
    by_org: [{team: {id: 'A', name: 'Bittrix'}, seconds: 75600}, {team: {id: 'B', name: 'InkYank'}, seconds: 7200}],
    has_expected: true,
    expected_seconds: 86400,
    expected_until: 'Thu 26 Nov',
    worked_until_yesterday: 61200,
    per_day_hours: 8,
    ...over,
});

test('hours: totals, expected, per day with modes, by organization; range tabs', async () => {
    mocked.fetchHours.mockResolvedValue(hours());
    render(<HoursPage teamId={TEAM}/>);

    expect(await screen.findByText('21h 0m', {selector: '.att-big'})).toBeInTheDocument();
    expect(screen.getByText('23h 0m', {selector: '.att-big'})).toBeInTheDocument();
    expect(screen.getByText('Up to Thu 26 Nov: 17h 0m of 24h 0m expected')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '71');
    const days = within(screen.getByLabelText('Per day')).getAllByRole('listitem');
    expect(days).toHaveLength(5); // the rest of the week is only in the chart
    expect(days[0]).toHaveTextContent('Mon 238h 0m🏢');
    expect(days[1]).toHaveTextContent('–');
    expect(days[2]).toHaveTextContent('🏠');
    expect(days[3]).toHaveTextContent('Thu 26 🎉');
    expect(days[4]).toHaveTextContent('● now');
    expect(screen.getByText('InkYank')).toBeInTheDocument();
    expect(mocked.fetchHours).toHaveBeenLastCalledWith(TEAM, 'week');

    mocked.fetchHours.mockResolvedValue(hours({range: 'month', has_expected: false, by_org: [], days: hours().days.slice(0, 5)}));
    await userEvent.click(screen.getByRole('tab', {name: 'Month'}));
    expect(mocked.fetchHours).toHaveBeenLastCalledWith(TEAM, 'month');
    expect(await screen.findByRole('tab', {name: 'Month'})).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    expect(screen.queryByRole('figure')).not.toBeInTheDocument(); // the chart is for the week
});

test('hours: the week chart has a column per day, readable by hover and keyboard', async () => {
    mocked.fetchHours.mockResolvedValue(hours());
    render(<HoursPage teamId={TEAM}/>);
    const chart = await screen.findByRole('figure', {name: 'Hours per day, Mon 23 Nov – Fri 27 Nov'});
    const cols = within(chart).getAllByRole('listitem');
    expect(cols).toHaveLength(7);
    expect(cols[0]).toHaveAccessibleName('Mon 23 · 8h 0m · office');
    expect(cols[3]).toHaveAccessibleName('Thu 26 · no time · holiday: Festival');
    expect(cols[4]).toHaveAccessibleName('Fri 27 · 6h 0m · office · still checked in');
    expect(cols[5]).toHaveAccessibleName('Sat 28 · later this week');
    expect(within(chart).getByText('8h expected a day')).toBeInTheDocument();

    await userEvent.hover(cols[2]);
    expect(within(chart).getByRole('tooltip')).toHaveTextContent('Wed 25 · 7h 0m · from home');

    // Keyboard: Tab to a day, arrows to move along.
    await userEvent.unhover(cols[2]);
    act(() => cols[0].focus());
    await userEvent.keyboard('{ArrowRight}');
    expect(cols[1]).toHaveFocus();
    expect(within(chart).getByRole('tooltip')).toHaveTextContent('Tue 24 · no time');
    await userEvent.keyboard('{Escape}');
    expect(within(chart).queryByRole('tooltip')).not.toBeInTheDocument();

    // No expected line for untracked members.
    mocked.fetchHours.mockResolvedValue(hours({has_expected: false}));
    await userEvent.click(screen.getByRole('tab', {name: 'Today'}));
    await userEvent.click(screen.getByRole('tab', {name: 'Week'}));
    expect(await screen.findByRole('figure')).toBeInTheDocument();
    expect(screen.queryByText('8h expected a day')).not.toBeInTheDocument();
});

test('hours: empty week says so', async () => {
    const empty = hours().days.map((d) => ({...d, seconds: 0, open: false, modes: []}));
    mocked.fetchHours.mockResolvedValue(hours({days: empty, team_seconds: 0, all_seconds: 0}));
    render(<HoursPage teamId={TEAM}/>);
    expect(await screen.findByText('No time recorded yet.')).toBeInTheDocument();
});

const wfh = (requests: api.WFHItem[]): api.WFHState => ({team: {id: TEAM, name: 'Bittrix'}, today: '2026-11-27', pending: requests.filter((r) => r.status === 'pending').length, requests});
const item = (id: number, status: string, over: Partial<api.WFHItem> = {}): api.WFHItem => ({
    id, dates: `Mon ${id} Dec`, start: '2026-12-01', end: '2026-12-01', half_day: false, reason: 'Plumber', status, can_cancel: status === 'pending', ...over,
});

test('wfh: list with status, decided by, cancel with confirmation', async () => {
    mocked.fetchWFH.mockResolvedValue(wfh([item(1, 'pending'), item(2, 'approved', {decided_by: 'janarthanan'}), item(3, 'rejected', {decided_by: 'barath', note: 'client demo'})]));
    mocked.cancelWFH.mockResolvedValue({message: 'cancelled', wfh: wfh([item(1, 'cancelled', {can_cancel: false})])});
    render(<WFHPage teamId={TEAM}/>);

    const rows = await screen.findAllByRole('listitem');
    expect(rows[0]).toHaveTextContent('Pending');
    expect(rows[1]).toHaveTextContent('Approvedby @janarthanan');
    expect(rows[2]).toHaveTextContent('Rejectedby @barath: client demo');
    expect(within(rows[1]).queryByText('Cancel')).not.toBeInTheDocument();

    await userEvent.click(within(rows[0]).getByText('Cancel'));
    expect(mocked.cancelWFH).not.toHaveBeenCalled(); // asks first
    await userEvent.click(screen.getByText('Yes, cancel it'));
    expect(mocked.cancelWFH).toHaveBeenCalledWith(TEAM, 1);
    expect(await screen.findByText('Cancelled')).toBeInTheDocument();
});

test('wfh: request form sends; field errors from the bot shown; half day only for one day', async () => {
    mocked.fetchWFH.mockResolvedValue(wfh([]));
    mocked.requestWFH.mockRejectedValueOnce(new api.FormError('x', {start_date: 'You already have a pending or approved request covering these dates.'}));
    mocked.requestWFH.mockResolvedValueOnce(wfh([item(4, 'pending')]));
    render(<WFHPage teamId={TEAM}/>);

    expect(await screen.findByText(/^No requests yet\. Ask for a day/)).toBeInTheDocument();
    await userEvent.click(screen.getByText('+ Request work from home'));
    const form = within(screen.getByRole('form', {name: 'Request work from home'}));
    const [from, to] = form.getAllByDisplayValue('');
    await userEvent.type(from, '2026-12-01');
    await userEvent.type(form.getByPlaceholderText(/Plumber/), 'Plumber visit');
    await userEvent.click(form.getByRole('checkbox'));
    await userEvent.click(form.getByText('Send request'));
    expect(mocked.requestWFH).toHaveBeenCalledWith(TEAM, {start_date: '2026-12-01', end_date: '', half_day: true, reason: 'Plumber visit'});
    expect(await screen.findByText(/already have a pending or approved request/)).toBeInTheDocument();

    await userEvent.type(to, '2026-12-03');
    expect(form.getByRole('checkbox')).toBeDisabled(); // half day is for one day
    await userEvent.click(form.getByText('Send request'));
    expect(mocked.requestWFH).toHaveBeenLastCalledWith(TEAM, {start_date: '2026-12-01', end_date: '2026-12-03', half_day: false, reason: 'Plumber visit'});
    expect(await screen.findByText(/Request sent to the admins/)).toBeInTheDocument();
    expect(screen.getByText('Pending')).toBeInTheDocument();
});

test('settings: GitHub link, my settings, connection checks', async () => {
    mocked.fetchSettings.mockResolvedValue({team: {id: TEAM, name: 'Bittrix'}, is_admin: false, github_set_up: true, github_org: 'bittrixtechnologies', github_login: 'vairabarath', expected_hours: 8, tracked: true, reminders_on: false});
    mockedClient.fetchHealth.mockResolvedValue({ok: true, version: '0.7.0'});
    render(<SettingsPage teamId={TEAM}/>);

    expect(await screen.findByRole('link', {name: '✅ vairabarath'})).toHaveAttribute('href', 'https://github.com/vairabarath');
    expect(screen.getByText('bittrixtechnologies')).toBeInTheDocument();
    expect(screen.getByText('8h (all organizations)')).toBeInTheDocument();
    expect(screen.getByText('Off')).toBeInTheDocument();
    const checks = within(screen.getByLabelText('Connection'));
    expect(await checks.findByText('Plugin 0.7.0')).toBeInTheDocument();
    expect(checks.getByText('Attendance service')).toBeInTheDocument();
    expect(checks.getByText('GitHub project for Bittrix')).toBeInTheDocument();
});

test('settings: not linked; service unreachable', async () => {
    mocked.fetchSettings.mockRejectedValueOnce(new Error('Couldn’t reach the attendance service.'));
    mockedClient.fetchHealth.mockResolvedValue({ok: false, version: '0.7.0', error: 'the shared secret is missing'});
    render(<SettingsPage teamId={TEAM}/>);
    expect(await screen.findByText('Attendance service: Couldn’t reach the attendance service.')).toBeInTheDocument();
    expect(await screen.findByText('Plugin: the shared secret is missing')).toBeInTheDocument();

    mocked.fetchSettings.mockResolvedValue({team: {id: TEAM, name: 'Bittrix'}, is_admin: false, github_set_up: true, github_org: 'o', expected_hours: 8, tracked: true, reminders_on: true});
    mockedClient.fetchHealth.mockResolvedValue({ok: true, version: '0.7.0'});
    await userEvent.click(screen.getByText('Check again'));
    expect(await screen.findByText(/Not linked yet/)).toBeInTheDocument();
});

test('a page that fails to load says why, and Try again reloads it', async () => {
    mocked.fetchHours.mockRejectedValueOnce(new Error('The attendance service is not reachable.'));
    mocked.fetchHours.mockResolvedValue(hours());
    render(<HoursPage teamId={TEAM}/>);
    expect(await screen.findByRole('alert')).toHaveTextContent('The attendance service is not reachable. Try again');
    await userEvent.click(screen.getByRole('button', {name: 'Try again'}));
    expect(await screen.findByRole('figure')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

test('week chart tooltip stays inside the chart: anchored at the column, growing inward', () => {
    expect(tipPosition(0, 7)).toEqual({left: '0%', maxWidth: '100%'});
    expect(tipPosition(6, 7)).toEqual({right: '0%', maxWidth: '100%'});
    const sat = tipPosition(5, 7);
    expect(sat.right).toMatch(/^14\.28/);
    expect(parseFloat(String(sat.maxWidth))).toBeCloseTo(85.71, 1);
});

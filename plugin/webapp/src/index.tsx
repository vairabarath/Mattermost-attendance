// The Attendance plugin's web part: an app-bar icon that opens the panel in
// the right-hand side, and live updates from the server. See ui-plan.md.

// Compass tokens are declared in cascade layers, so this is safe to load next to the host's
// copy. Never import '@mattermost/compass-ui/styles/standalone' here: it overrides the user's theme.
import '@mattermost/compass-ui/styles';

import {publicURL, setSiteURL} from 'client';
import manifest from 'manifest';
import Panel, {REFRESH_EVENT} from 'panel/Panel';
import type {Store} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

import {getConfig} from 'mattermost-redux/selectors/entities/general';

import type {PluginRegistry} from 'types/mattermost-webapp';

// Events the bot sends (through the plugin) when something changed.
export const LIVE_EVENTS = ['session_changed', 'tasks_changed', 'eod_changed', 'wfh_changed', 'team_changed'];

const refresh = () => window.dispatchEvent(new Event(REFRESH_EVENT));

export default class Plugin {
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        setSiteURL(getConfig(store.getState()).SiteURL);

        // The panel lives in the right-hand side; the app-bar icon toggles it.
        const {toggleRHSPlugin} = registry.registerRightHandSidebarComponent(Panel, 'Attendance');
        registry.registerAppBarComponent({
            iconUrl: publicURL('icon.svg'),
            action: () => store.dispatch(toggleRHSPlugin),
            tooltipText: 'Attendance',
            supportedProductIds: null,
        });

        for (const ev of LIVE_EVENTS) {
            registry.registerWebSocketEventHandler(`custom_${manifest.id}_${ev}`, refresh);
        }

        // Events sent while disconnected are lost: reload when we're back.
        registry.registerReconnectHandler(refresh);
    }
}

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

window.registerPlugin(manifest.id, new Plugin());

import React, { useState } from 'react';
import {
  StyleSheet,
  Text,
  View,
  TouchableOpacity,
  TextInput,
  ScrollView,
  SafeAreaView,
  StatusBar,
} from 'react-native';
import { SpineProvider, useSpineState, useSpineEvent, useSpineContext } from './SpineSDK';

function MobileHeader() {
  const { connected } = useSpineContext();

  return (
    <View style={styles.header}>
      <View style={styles.brandRow}>
        <View style={styles.logoBadge}>
          <Text style={styles.logoIcon}>⚡</Text>
        </View>
        <View>
          <Text style={styles.title}>Spine Android</Text>
          <Text style={styles.subtitle}>Native React Mobile Engine</Text>
        </View>
      </View>
      <View style={[styles.statusBadge, connected ? styles.statusConnected : styles.statusDisconnected]}>
        <View style={[styles.dot, connected ? styles.dotConnected : styles.dotDisconnected]} />
        <Text style={[styles.statusText, connected ? styles.textConnected : styles.textDisconnected]}>
          {connected ? 'Spine Online' : 'Connecting'}
        </Text>
      </View>
    </View>
  );
}

function MobileLeadForm() {
  const emit = useSpineEvent();
  const [email, setEmail] = useState('android.lead@spine.dev');
  const [name, setName] = useState('Android Device User');
  const [loading, setLoading] = useState(false);

  const handleEmit = async () => {
    setLoading(true);
    try {
      await emit('SUBMIT_LEAD', { email, name });
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  return (
    <View style={styles.card}>
      <Text style={styles.cardTitle}>📱 Emit Mobile Event (SUBMIT_LEAD)</Text>
      <Text style={styles.cardDesc}>Triggers Go event bus step pipeline from Android native UI.</Text>

      <TextInput
        style={styles.input}
        value={name}
        onChangeText={setName}
        placeholder="User Name"
        placeholderTextColor="#64748b"
      />
      <TextInput
        style={styles.input}
        value={email}
        onChangeText={setEmail}
        placeholder="Email Address"
        placeholderTextColor="#64748b"
        keyboardType="email-address"
      />

      <TouchableOpacity
        style={[styles.btn, styles.btnPrimary]}
        onPress={handleEmit}
        disabled={loading}
      >
        <Text style={styles.btnText}>
          {loading ? 'Emitting...' : '⚡ Fire Event from Android'}
        </Text>
      </TouchableOpacity>
    </View>
  );
}

function MobileLiveStateBadge() {
  const leadStatus = useSpineState('LEAD_STATUS');

  return (
    <View style={[styles.card, styles.highlightCard]}>
      <Text style={styles.cardTitle}>📡 Live Subscribed State (LEAD_STATUS)</Text>
      <Text style={styles.cardDesc}>Receives sub-3ms Spine WebSocket push directly on Android.</Text>

      <View style={styles.stateBox}>
        {leadStatus ? (
          <View>
            <Text style={styles.badgeSuccess}>✓ WEBSOCKET PUSH RECEIVED</Text>
            <Text style={styles.stateText}>{JSON.stringify(leadStatus, null, 2)}</Text>
          </View>
        ) : (
          <Text style={styles.placeholderText}>Waiting for Spine state push...</Text>
        )}
      </View>
    </View>
  );
}

function MobileItemController() {
  const emit = useSpineEvent();
  const itemState = useSpineState('ITEM_UPDATED');

  return (
    <View style={styles.card}>
      <Text style={styles.cardTitle}>⚙️ Dynamic State Controller (ITEM_UPDATED)</Text>
      <Text style={styles.cardDesc}>Low-latency state push with 0 battery polling overhead.</Text>

      <View style={styles.btnGroup}>
        <TouchableOpacity
          style={[styles.btn, styles.btnSecondary]}
          onPress={() => emit('UPDATE_ITEM', { id: 'android-01', value: 'High Speed Mode' })}
        >
          <Text style={styles.btnSecondaryText}>⚡ High Speed</Text>
        </TouchableOpacity>

        <TouchableOpacity
          style={[styles.btn, styles.btnSecondary]}
          onPress={() => emit('UPDATE_ITEM', { id: 'android-01', value: 'Turbo Mode' })}
        >
          <Text style={styles.btnSecondaryText}>🚀 Turbo Mode</Text>
        </TouchableOpacity>
      </View>

      <View style={styles.compactBox}>
        <Text style={styles.label}>STATE: </Text>
        <Text style={styles.valText}>{itemState ? itemState.value : 'Idle'}</Text>
      </View>
    </View>
  );
}

function MainMobileApp() {
  return (
    <SafeAreaView style={styles.safeArea}>
      <StatusBar barStyle="light-content" backgroundColor="#060913" />
      <ScrollView contentContainerStyle={styles.container}>
        <MobileHeader />
        <MobileLeadForm />
        <MobileLiveStateBadge />
        <MobileItemController />
      </ScrollView>
    </SafeAreaView>
  );
}

export default function App() {
  return (
    <SpineProvider url="http://172.16.1.145:8080">
      <MainMobileApp />
    </SpineProvider>
  );
}

const styles = StyleSheet.create({
  safeArea: {
    flex: 1,
    backgroundColor: '#060913',
  },
  container: {
    padding: 20,
    gap: 16,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 10,
    paddingBottom: 15,
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(255,255,255,0.08)',
  },
  brandRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
  },
  logoBadge: {
    width: 44,
    height: 44,
    borderRadius: 12,
    backgroundColor: '#6366f1',
    alignItems: 'center',
    justifyContent: 'center',
  },
  logoIcon: {
    fontSize: 22,
  },
  title: {
    color: '#ffffff',
    fontSize: 20,
    fontWeight: 'bold',
  },
  subtitle: {
    color: '#94a3b8',
    fontSize: 12,
  },
  statusBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 20,
    gap: 6,
    borderWidth: 1,
  },
  statusConnected: {
    borderColor: '#10b981',
    backgroundColor: 'rgba(16,185,129,0.1)',
  },
  statusDisconnected: {
    borderColor: '#ef4444',
    backgroundColor: 'rgba(239,68,68,0.1)',
  },
  dot: {
    width: 8,
    height: 8,
    borderRadius: 4,
  },
  dotConnected: {
    backgroundColor: '#10b981',
  },
  dotDisconnected: {
    backgroundColor: '#ef4444',
  },
  statusText: {
    fontSize: 12,
    fontWeight: '600',
  },
  textConnected: {
    color: '#10b981',
  },
  textDisconnected: {
    color: '#ef4444',
  },
  card: {
    backgroundColor: '#0d1321',
    borderRadius: 16,
    padding: 18,
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.08)',
  },
  highlightCard: {
    borderColor: 'rgba(99,102,241,0.4)',
    backgroundColor: '#111827',
  },
  cardTitle: {
    color: '#ffffff',
    fontSize: 16,
    fontWeight: 'bold',
    marginBottom: 4,
  },
  cardDesc: {
    color: '#94a3b8',
    fontSize: 13,
    marginBottom: 14,
  },
  input: {
    backgroundColor: '#030712',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.1)',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 10,
    color: '#ffffff',
    fontSize: 14,
    marginBottom: 10,
  },
  btn: {
    paddingVertical: 12,
    paddingHorizontal: 16,
    borderRadius: 10,
    alignItems: 'center',
    justifyContent: 'center',
  },
  btnPrimary: {
    backgroundColor: '#6366f1',
  },
  btnText: {
    color: '#ffffff',
    fontWeight: 'bold',
    fontSize: 14,
  },
  btnGroup: {
    flexDirection: 'row',
    gap: 10,
    marginBottom: 12,
  },
  btnSecondary: {
    flex: 1,
    backgroundColor: 'rgba(255,255,255,0.06)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.1)',
  },
  btnSecondaryText: {
    color: '#f8fafc',
    fontWeight: '600',
    fontSize: 13,
  },
  stateBox: {
    backgroundColor: '#030712',
    borderRadius: 10,
    padding: 12,
    minHeight: 80,
  },
  badgeSuccess: {
    color: '#10b981',
    fontWeight: 'bold',
    fontSize: 11,
    marginBottom: 6,
  },
  stateText: {
    color: '#a5b4fc',
    fontFamily: 'monospace',
    fontSize: 12,
  },
  placeholderText: {
    color: '#64748b',
    fontSize: 13,
  },
  compactBox: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#030712',
    padding: 12,
    borderRadius: 10,
  },
  label: {
    color: '#94a3b8',
    fontSize: 12,
    fontWeight: 'bold',
  },
  valText: {
    color: '#10b981',
    fontWeight: 'bold',
    fontSize: 14,
  },
});

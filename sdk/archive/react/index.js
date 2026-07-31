
var React = require('react');

var SpineContext = React.createContext(null);

function SpineProvider(props) {
  var url = props.url;
  var children = props.children;
  var connectedState = React.useState(false);
  var connected = connectedState[0];
  var setConnected = connectedState[1];
  var listenersRef = React.useRef(new Map());
  var wsRef = React.useRef(null);

  var httpUrl = url.replace(/^ws:/, 'http:').replace(/^wss:/, 'https:').replace(/\/ws$/, '');
  var wsUrl = url.startsWith('http')
    ? url.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:') + '/ws'
    : url;

  React.useEffect(function() {
    var ws;
    var timer;
    function connect() {
      try {
        ws = new WebSocket(wsUrl);
        wsRef.current = ws;
        ws.onopen = function() { setConnected(true); };
        ws.onmessage = function(e) {
          try {
            var data = JSON.parse(e.data);
            var stateName = data.state || data.event;
            if (stateName && listenersRef.current.has(stateName)) {
              listenersRef.current.get(stateName).forEach(function(cb) { cb(data.payload || data); });
            }
          } catch(err) {}
        };
        ws.onclose = function() {
          setConnected(false);
          wsRef.current = null;
          timer = setTimeout(connect, 2000);
        };
      } catch(e) { setConnected(false); }
    }
    connect();
    return function() { clearTimeout(timer); if (wsRef.current) wsRef.current.close(); };
  }, [wsUrl]);

  var emit = async function(eventName, payload) {
    var res = await fetch(httpUrl + '/emit', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ event: eventName, payload: payload })
    });
    return await res.json();
  };

  var subscribe = function(stateName, listener) {
    if (!listenersRef.current.has(stateName)) {
      listenersRef.current.set(stateName, new Set());
    }
    listenersRef.current.get(stateName).add(listener);
    return function() {
      var set = listenersRef.current.get(stateName);
      if (set) {
        set.delete(listener);
        if (set.size === 0) listenersRef.current.delete(stateName);
      }
    };
  };

  return React.createElement(SpineContext.Provider, {
    value: { connected: connected, emit: emit, subscribe: subscribe, serverUrl: httpUrl }
  }, children);
}

function useSpineContext() {
  var context = React.useContext(SpineContext);
  if (!context) throw new Error('useSpineContext must be used within a <SpineProvider>');
  return context;
}

function useSpineState(stateName, initialValue) {
  var ctx = useSpineContext();
  var stateVal = React.useState(initialValue);
  var state = stateVal[0];
  var setState = stateVal[1];

  React.useEffect(function() {
    return ctx.subscribe(stateName, function(data) { setState(data); });
  }, [stateName, ctx.subscribe]);

  return state;
}

function useSpineEvent() {
  return useSpineContext().emit;
}

module.exports = {
  SpineProvider: SpineProvider,
  useSpineContext: useSpineContext,
  useSpineState: useSpineState,
  useSpineEvent: useSpineEvent
};

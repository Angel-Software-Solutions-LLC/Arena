(function attachArenaEmbeddedCheckout(root) {
  'use strict';

  const STRIPE_SCRIPT_URL = 'https://js.stripe.com/clover/stripe.js';
  const CHECKOUT_CONFIG_PATH = '/api/v1/cosmetics/checkout/config';
  const DIALOG_ID = 'arena-embedded-checkout-dialog';
  const MOUNT_ID = 'arena-embedded-checkout-mount';
  const MESSAGE = Object.freeze({
    prepare: 'arena:stripe-checkout:prepare',
    ready: 'arena:stripe-checkout:ready',
    mount: 'arena:stripe-checkout:mount',
    mounted: 'arena:stripe-checkout:mounted',
    abort: 'arena:stripe-checkout:abort',
    complete: 'arena:stripe-checkout:complete',
    error: 'arena:stripe-checkout:error',
  });

  let configPromise = null;
  let stripeScriptPromise = null;
  let stripeInstance = null;
  let checkoutInstance = null;
  let activeMountedSessionID = '';
  let activeParentCheckout = null;
  let localCheckoutGeneration = 0;
  let completionCloseTimer = null;
  const childWaiters = new Map();
  const childReservations = new Set();
  const activeChildReservations = new Set();
  const preparedFrames = new Map();

  function isTopLevel() {
    try {
      return root.top === root.self;
    } catch (_) {
      return false;
    }
  }

  function checkoutConfigURL(pathname) {
    const currentPath = typeof pathname === 'string'
      ? pathname
      : String(root.location?.pathname || '/');
    const mountPrefix = currentPath === '/arena' || currentPath.startsWith('/arena/')
      ? '/arena'
      : '';
    return `${mountPrefix}${CHECKOUT_CONFIG_PATH}`;
  }

  function normalizeMountPayload(raw) {
    if (!raw || raw.presentation !== 'embedded') return null;
    const sessionID = String(raw.session_id || '').trim();
    const clientSecret = String(raw.client_secret || '').trim();
    if (!sessionID || sessionID.length > 255 || !clientSecret || clientSecret.length > 2048) return null;
    if (!sessionID.startsWith('cs_') || !clientSecret.startsWith(`${sessionID}_secret_`)) return null;
    return {presentation:'embedded',session_id:sessionID,client_secret:clientSecret};
  }

  function normalizeReservation(raw) {
    const requestID = String(raw?.request_id || '').trim();
    if (!requestID || requestID.length > 128) return null;
    return {request_id:requestID};
  }

  function completionCloseIsSafe(completedGeneration, currentGeneration, activeSessionID) {
	return completedGeneration === currentGeneration && String(activeSessionID || '') === '';
  }

  function isTrustedDashboardMessage(event, frame, expectedOrigin) {
    return Boolean(
      event && frame && frame.contentWindow &&
      event.origin === expectedOrigin && event.source === frame.contentWindow
    );
  }

  root.ArenaEmbeddedCheckoutInternals = Object.freeze({
    MESSAGE,
    checkoutConfigURL,
    normalizeMountPayload,
    normalizeReservation,
	completionCloseIsSafe,
    isTrustedDashboardMessage,
  });

  function checkoutError(message) {
    const error = new Error(message);
    error.name = 'ArenaCheckoutError';
    return error;
  }

  /**
   * Where buying happens when it does not happen here, or ''.
   *
   * The value comes from Arena's own checkout config, so this is a floor under
   * a misconfigured deployment rather than a defence against a hostile one: a
   * Buy control that does nothing beats one that navigates somewhere
   * unintended, and only an https origin is an address worth leaving for.
   */
  function handoffURL(value) {
    const raw = typeof value === 'string' ? value.trim() : '';
    if (!raw.startsWith('https://') || raw.length <= 'https://'.length) return '';
    return /[\s"'<>]/.test(raw) ? '' : raw;
  }

  function clearCompletionCloseTimer() {
	if (completionCloseTimer !== null && typeof root.clearTimeout === 'function') {
	  root.clearTimeout(completionCloseTimer);
	}
	completionCloseTimer = null;
  }

  function beginLocalCheckoutGeneration() {
	clearCompletionCloseTimer();
	localCheckoutGeneration += 1;
	return localCheckoutGeneration;
  }

  function requireLocalCheckoutGeneration(generation) {
	if (generation === localCheckoutGeneration) return;
	throw checkoutError('Checkout replaced by a newer request.');
  }

  function post(target, origin, payload) {
    if (target && typeof target.postMessage === 'function') target.postMessage(payload, origin);
  }

  function dispatchCompletion(payload) {
    if (typeof root.CustomEvent !== 'function' || typeof root.dispatchEvent !== 'function') return;
    root.dispatchEvent(new root.CustomEvent('arena:stripe-checkout:complete', {detail:payload}));
  }

  function dispatchAbort(payload) {
    if (typeof root.CustomEvent !== 'function' || typeof root.dispatchEvent !== 'function') return;
    root.dispatchEvent(new root.CustomEvent('arena:stripe-checkout:abort', {detail:payload}));
  }

  function ensureDialog() {
    const doc = root.document;
    if (!doc || !doc.body || typeof doc.createElement !== 'function') {
      throw checkoutError('Secure checkout is unavailable in this browser.');
    }
    let dialog = doc.getElementById(DIALOG_ID);
    if (dialog) return dialog;

    dialog = doc.createElement('dialog');
    dialog.id = DIALOG_ID;
    dialog.className = 'arena-stripe-dialog';
    dialog.setAttribute('aria-labelledby', 'arena-stripe-dialog-title');
    dialog.innerHTML = [
      '<section class="arena-stripe-panel">',
      '  <header class="arena-stripe-header">',
      '    <div>',
      '      <span class="arena-stripe-kicker">Arena outfitter / secure checkout</span>',
      '      <h2 id="arena-stripe-dialog-title">Complete your loadout</h2>',
      '      <p>Payment details go directly to Stripe. Arena unlocks cosmetics only after the signed payment event is verified.</p>',
      '    </div>',
      '    <button class="arena-stripe-close" type="button" aria-label="Close secure checkout">&times;</button>',
      '  </header>',
      '  <div class="arena-stripe-status" role="status" aria-live="polite">Preparing secure checkout&hellip;</div>',
      `  <div id="${MOUNT_ID}" class="arena-stripe-mount" aria-label="Stripe secure checkout"></div>`,
      '  <footer class="arena-stripe-footer">',
      '    <span aria-hidden="true">&#9679;</span> Encrypted by Stripe',
      '    <span>Cosmetics never affect combat stats.</span>',
      '  </footer>',
      '</section>',
    ].join('');
    doc.body.appendChild(dialog);
    dialog.querySelector('.arena-stripe-close')?.addEventListener('click', () => abort(''));
    dialog.addEventListener('cancel', event => {
      event.preventDefault();
      abort('');
    });
    return dialog;
  }

  function setDialogState(state, message) {
    const dialog = ensureDialog();
    dialog.dataset.state = state;
    const status = dialog.querySelector('.arena-stripe-status');
    if (status) {
      status.textContent = message || '';
      status.hidden = !message;
    }
    if (!dialog.open && typeof dialog.showModal === 'function') dialog.showModal();
    return dialog;
  }

  async function destroyCheckout() {
    const instance = checkoutInstance;
    checkoutInstance = null;
    if (!instance || typeof instance.destroy !== 'function') return;
    try {
      await instance.destroy();
    } catch (_) {
      // A Stripe iframe can already be gone after a redirect or completion.
    }
  }

  async function closeLocalDialog() {
	clearCompletionCloseTimer();
    await destroyCheckout();
    activeMountedSessionID = '';
    const dialog = root.document?.getElementById?.(DIALOG_ID);
    const mount = root.document?.getElementById?.(MOUNT_ID);
    if (mount) mount.replaceChildren();
    if (dialog?.open && typeof dialog.close === 'function') dialog.close();
    if (dialog) delete dialog.dataset.state;
  }

  function loadStripeScript() {
    if (typeof root.Stripe === 'function') return Promise.resolve(root.Stripe);
    if (stripeScriptPromise) return stripeScriptPromise;
    const doc = root.document;
    if (!doc?.head || typeof doc.createElement !== 'function') {
      return Promise.reject(checkoutError('Stripe could not load in this browser.'));
    }
    stripeScriptPromise = new Promise((resolve, reject) => {
      let script = doc.querySelector(`script[src="${STRIPE_SCRIPT_URL}"]`);
      if (!script) {
        script = doc.createElement('script');
        script.src = STRIPE_SCRIPT_URL;
        script.async = true;
        script.dataset.arenaStripe = 'true';
        doc.head.appendChild(script);
      }
      const timer = typeof root.setTimeout === 'function'
        ? root.setTimeout(() => reject(checkoutError('Stripe took too long to load. Try again.')), 15000)
        : null;
      const finish = callback => {
        if (timer !== null && typeof root.clearTimeout === 'function') root.clearTimeout(timer);
        callback();
      };
      script.addEventListener('load', () => finish(() => {
        if (typeof root.Stripe === 'function') resolve(root.Stripe);
        else reject(checkoutError('Stripe loaded without a checkout client.'));
      }), {once:true});
      script.addEventListener('error', () => finish(() => reject(checkoutError('Stripe could not load. Check your connection and try again.'))), {once:true});
    }).catch(error => {
      stripeScriptPromise = null;
      throw error;
    });
    return stripeScriptPromise;
  }

  function loadCheckoutConfig() {
    if (configPromise) return configPromise;
    if (typeof root.fetch !== 'function') return Promise.reject(checkoutError('Checkout configuration is unavailable.'));
    configPromise = root.fetch(checkoutConfigURL(), {credentials:'same-origin',headers:{Accept:'application/json'}})
      .then(async response => {
        const data = await response.json().catch(() => ({}));
        /*
         * Purchasing moved to the Angel account. Recognised here, before the
         * "is checkout enabled" test below, because in handoff mode `enabled`
         * is deliberately false — a browser running an older bundle should
         * stop offering a checkout it cannot complete, and one running this
         * bundle should send the buyer somewhere instead of reporting that
         * checkout is off.
         */
        if (data?.mode === 'handoff') {
          const handoff = handoffURL(data.handoff_url);
          if (handoff) return {handoff};
          // Handoff mode with no usable destination is a misconfiguration, and
          // it is reported as one. Falling through would test `enabled`, which
          // is deliberately false here, and the buyer would be told checkout
          // is off rather than that this deployment is wired wrong.
          throw checkoutError('Purchases have moved, but this Arena was not told where to.');
        }
        if (!response.ok || !data?.enabled) throw checkoutError(data?.error || 'Checkout is not enabled.');
        const publishableKey = String(data.publishable_key || '').trim();
        if (!/^pk_(test|live)_/.test(publishableKey)) throw checkoutError('Checkout returned an invalid browser key.');
        return {publishableKey,defaultPresentation:String(data.default_presentation || 'embedded')};
      })
      .catch(error => {
        configPromise = null;
        throw error;
      });
    return configPromise;
  }

  async function prepareLocal() {
	const generation = beginLocalCheckoutGeneration();
    setDialogState('preparing', 'Preparing secure checkout…');
    try {
      /*
       * Both are started together, and the configuration is read first.
       *
       * Together, because in the ordinary case Stripe's script and this
       * config are two independent round trips and serialising them would
       * cost a visible pause before the payment form appears. Config first,
       * because it is the answer that decides whether the script is wanted at
       * all: once purchasing has moved there is no form to fill in here, and
       * waiting on a payment library to find that out would be a delay in
       * service of nothing.
       */
      const configReady = loadCheckoutConfig();
      const scriptReady = loadStripeScript();
      // A script failure is irrelevant if we are about to leave, but an
      // unhandled rejection is not; this claims it either way.
      scriptReady.catch(() => {});
      const config = await configReady;
      if (config.handoff) {
        setDialogState('ready', 'Opening your Angel account…');
        root.location.assign(config.handoff);
        /*
         * A browser has left the page by now. The throw is for everything
         * that has not — a blocked navigation, a test — so that no caller
         * goes on to mount a payment form for a purchase happening on another
         * origin. It carries a marker so the handler below can tell it from a
         * failure: nothing went wrong, and the dialog already says where the
         * buyer is going.
         */
        const handedOff = checkoutError('Purchases are completed in your Angel account.');
        handedOff.handoff = config.handoff;
        throw handedOff;
      }
      const Stripe = await scriptReady;
	  requireLocalCheckoutGeneration(generation);
      if (!stripeInstance) stripeInstance = Stripe(config.publishableKey);
      if (!stripeInstance || typeof stripeInstance.initEmbeddedCheckout !== 'function') {
        throw checkoutError('Stripe Embedded Checkout is unavailable.');
      }
      setDialogState('ready', 'Secure checkout ready. Opening payment form…');
	  return generation;
    } catch (error) {
	  if (!error?.handoff && generation === localCheckoutGeneration) {
		setDialogState('error', error?.message || 'Secure checkout could not open.');
	  }
      throw error;
    }
  }

  function frameForDashboard() {
    return root.document?.getElementById?.('dashboard-frame') || null;
  }

  function childRequest(type, payload) {
    const requestID = payload?.request_id || root.crypto?.randomUUID?.() || `checkout-${Date.now()}-${Math.random()}`;
    return new Promise((resolve, reject) => {
      const timer = typeof root.setTimeout === 'function' ? root.setTimeout(() => {
        childWaiters.delete(requestID);
        reject(checkoutError('Arena checkout did not respond. Try again.'));
      }, 15000) : null;
      childWaiters.set(requestID, {resolve,reject,timer,type});
      post(root.parent, root.location.origin, {...payload,type,request_id:requestID});
    });
  }

  async function prepare() {
    if (isTopLevel()) {
      await prepareLocal();
      return Object.freeze({request_id:'local'});
    }
    const result = await childRequest(MESSAGE.prepare, {});
    const reservation = normalizeReservation(result);
    if (!reservation) throw checkoutError('Arena checkout returned an invalid reservation.');
    childReservations.add(reservation.request_id);
    return Object.freeze(reservation);
  }

  async function handleLocalComplete(payload) {
	const generation = localCheckoutGeneration;
	if (!payload || activeMountedSessionID !== payload.session_id) return;
    setDialogState('complete', 'Payment submitted. Arena is verifying Stripe’s signed confirmation…');
    await destroyCheckout();
	if (generation !== localCheckoutGeneration || activeMountedSessionID !== payload.session_id) return;
	activeMountedSessionID = '';
    dispatchCompletion(payload);
	if (activeParentCheckout?.sessionID === payload.session_id) {
      post(activeParentCheckout.source, activeParentCheckout.origin, {
        type:MESSAGE.complete,
        request_id:activeParentCheckout.requestID,
        session_id:payload.session_id,
      });
      activeParentCheckout = null;
    }
	if (generation !== localCheckoutGeneration || typeof root.setTimeout !== 'function') return;
	completionCloseTimer = root.setTimeout(() => {
	  completionCloseTimer = null;
	  if (!completionCloseIsSafe(generation, localCheckoutGeneration, activeMountedSessionID)) return;
	  closeLocalDialog();
	}, 900);
  }

  async function mountLocal(rawPayload) {
    const payload = normalizeMountPayload(rawPayload);
    if (!payload) throw checkoutError('Checkout returned an invalid embedded session.');
	const generation = await prepareLocal();
    await destroyCheckout();
	requireLocalCheckoutGeneration(generation);
    const mountNode = root.document?.getElementById?.(MOUNT_ID);
    if (!mountNode) throw checkoutError('Secure checkout mount is unavailable.');
    mountNode.replaceChildren();
    setDialogState('mounting', 'Loading Stripe’s secure payment form…');
	const instance = await stripeInstance.initEmbeddedCheckout({
      fetchClientSecret: async () => payload.client_secret,
      onComplete: () => handleLocalComplete(payload),
    });
	if (generation !== localCheckoutGeneration) {
	  if (instance && typeof instance.destroy === 'function') {
		try { await instance.destroy(); } catch (_) {}
	  }
	  requireLocalCheckoutGeneration(generation);
	}
	if (!instance || typeof instance.mount !== 'function') {
      throw checkoutError('Stripe did not return a payment form.');
    }
	checkoutInstance = instance;
	checkoutInstance.mount(`#${MOUNT_ID}`);
    activeMountedSessionID = payload.session_id;
    setDialogState('mounted', '');
    return payload;
  }

  async function mount(payload, rawReservation) {
    if (isTopLevel()) return mountLocal(payload);
    const reservation = normalizeReservation(rawReservation);
    if (!reservation || !childReservations.has(reservation.request_id)) {
      throw checkoutError('Secure checkout was not prepared for this request.');
    }
    childReservations.delete(reservation.request_id);
	activeChildReservations.add(reservation.request_id);
    const result = await childRequest(MESSAGE.mount, {...payload,request_id:reservation.request_id});
    return result;
  }

  async function abort(message, rawReservation) {
    if (!isTopLevel()) {
	  const reservation = normalizeReservation(rawReservation);
	  const requestIDs = reservation
		? [reservation.request_id]
		: [...new Set([...childReservations, ...activeChildReservations, ...childWaiters.keys()])];
	  for (const requestID of requestIDs) {
		if (!childReservations.has(requestID) && !activeChildReservations.has(requestID) && !childWaiters.has(requestID)) continue;
		post(root.parent, root.location.origin, {
		  type:MESSAGE.abort,request_id:requestID,message:String(message || ''),
		});
		childReservations.delete(requestID);
		activeChildReservations.delete(requestID);
      }
      return;
    }
	beginLocalCheckoutGeneration();
    const sessionID = String(activeParentCheckout?.sessionID || activeMountedSessionID || '');
	const activeRequestID = String(activeParentCheckout?.requestID || '');
    const abortMessage = String(message || 'Secure checkout closed.');
    if (activeParentCheckout) {
      post(activeParentCheckout.source, activeParentCheckout.origin, {
        type:MESSAGE.abort,
        request_id:activeParentCheckout.requestID,
        session_id:sessionID,
        message:abortMessage,
      });
    }
    for (const [requestID, source] of preparedFrames) {
      post(source, root.location?.origin, {
        type:MESSAGE.abort,
        request_id:requestID,
        session_id:'',
        message:abortMessage,
      });
    }
    preparedFrames.clear();
    activeParentCheckout = null;
	dispatchAbort({request_id:activeRequestID,session_id:sessionID,message:abortMessage});
    if (message) {
      await destroyCheckout();
      activeMountedSessionID = '';
      setDialogState('error', String(message));
      return;
    }
    await closeLocalDialog();
  }

  function resolveChildMessage(event) {
    if (!event || event.origin !== root.location?.origin || event.source !== root.parent) return false;
    const data = event.data || {};
    const requestID = String(data.request_id || '');
    const waiter = childWaiters.get(requestID);
    if (data.type === MESSAGE.complete) {
	  activeChildReservations.delete(requestID);
	  dispatchCompletion({presentation:'embedded',request_id:requestID,session_id:String(data.session_id || '')});
      return true;
    }
    if (data.type === MESSAGE.abort) {
      if (waiter) {
        if (waiter.timer !== null && typeof root.clearTimeout === 'function') root.clearTimeout(waiter.timer);
        childWaiters.delete(requestID);
        waiter.reject(checkoutError(String(data.message || 'Secure checkout closed.')));
      }
	  childReservations.delete(requestID);
	  activeChildReservations.delete(requestID);
      dispatchAbort({
		request_id:requestID,
        session_id:String(data.session_id || ''),
        message:String(data.message || 'Secure checkout closed.'),
      });
      return true;
    }
    if (!waiter) return false;
    if (waiter.timer !== null && typeof root.clearTimeout === 'function') root.clearTimeout(waiter.timer);
    childWaiters.delete(requestID);
	if (data.type === MESSAGE.error) {
	  childReservations.delete(requestID);
	  activeChildReservations.delete(requestID);
	}
    if (data.type === MESSAGE.error) waiter.reject(checkoutError(String(data.message || 'Secure checkout failed.')));
    else if (data.type === MESSAGE.ready || data.type === MESSAGE.mounted) waiter.resolve(data);
    else waiter.reject(checkoutError('Secure checkout returned an unexpected response.'));
    return true;
  }

  async function handleParentMessage(event) {
    if (!isTopLevel()) {
      resolveChildMessage(event);
      return;
    }
    const frame = frameForDashboard();
    if (!isTrustedDashboardMessage(event, frame, root.location?.origin)) return;
    const data = event.data || {};
    const requestID = String(data.request_id || '').trim();
    if (!requestID || requestID.length > 128) return;

    if (data.type === MESSAGE.prepare) {
	  if (activeParentCheckout?.source === event.source && activeParentCheckout.requestID !== requestID) {
		await abort('Checkout replaced by a newer request.');
	  }
	  for (const [preparedID, source] of preparedFrames) {
		if (source !== event.source || preparedID === requestID) continue;
		preparedFrames.delete(preparedID);
		post(source, event.origin, {
		  type:MESSAGE.abort,
		  request_id:preparedID,
		  session_id:'',
		  message:'Checkout replaced by a newer request.',
		});
	  }
      preparedFrames.set(requestID, event.source);
      try {
        await prepareLocal();
        if (preparedFrames.get(requestID) !== event.source) return;
        post(event.source, event.origin, {type:MESSAGE.ready,request_id:requestID});
      } catch (error) {
        preparedFrames.delete(requestID);
        post(event.source, event.origin, {type:MESSAGE.error,request_id:requestID,message:error?.message || 'Secure checkout could not open.'});
      }
      return;
    }
	if (data.type === MESSAGE.abort) {
	  const prepared = preparedFrames.get(requestID) === event.source;
	  const active = activeParentCheckout?.requestID === requestID && activeParentCheckout.source === event.source;
	  if (!prepared && !active) return;
	  if (prepared) preparedFrames.delete(requestID);
	  await abort(String(data.message || ''));
	  return;
	}
    if (data.type !== MESSAGE.mount || preparedFrames.get(requestID) !== event.source) return;
    preparedFrames.delete(requestID);
    try {
      const payload = normalizeMountPayload(data);
      if (!payload) throw checkoutError('Checkout returned an invalid embedded session.');
      activeParentCheckout = {requestID,source:event.source,origin:event.origin,sessionID:payload.session_id};
      await mountLocal(payload);
      post(event.source, event.origin, {type:MESSAGE.mounted,request_id:requestID,session_id:payload.session_id});
    } catch (error) {
      activeParentCheckout = null;
      post(event.source, event.origin, {type:MESSAGE.error,request_id:requestID,message:error?.message || 'Secure checkout could not mount.'});
    }
  }

  if (typeof root.addEventListener === 'function') root.addEventListener('message', handleParentMessage);
  root.ArenaEmbeddedCheckout = Object.freeze({prepare,mount,abort});
})(typeof window !== 'undefined' ? window : globalThis);

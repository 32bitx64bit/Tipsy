// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

const (
	webLoginInitSym      = "Java_com_roblox_universalapp_linking_JNIWebLoginProtocol_init"
	webLoginColdStartSym = "Java_com_roblox_universalapp_linking_JNIWebLoginProtocol_maybeHandleColdStartProtocolLaunch"
	baseURLInitSym       = "Java_com_roblox_universalapp_linking_JNIBaseUrlProtocol_init"
	baseURLColdStartSym  = "Java_com_roblox_universalapp_linking_JNIBaseUrlProtocol_maybeHandleColdStartProtocolLaunch"
	startGameSym         = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2StartGameWithParam"
)

func importWebsiteAuth(ctx context.Context, cookiePath string, req *rbxuri.Request) error {
	if req == nil || !req.HasTicket() {
		return nil
	}
	result, err := rbxuri.RedeemAuthenticationTicket(ctx, nil, req.Ticket)
	if err != nil {
		logging.Logger(logging.CatAuth).Info("website authentication ticket not redeemed", "err", err)
		return nil
	}
	if err := jni.ImportAuthSetCookies(cookiePath, "https://www.roblox.com/", result.Origin, result.SetCookie); err != nil {
		return fmt.Errorf("persist website session: %w", err)
	}
	req.TicketRedeemed = true
	logging.Logger(logging.CatAuth).Info("website authentication ticket redeemed")
	return nil
}

func handleColdStartProtocolLaunch(mod *loader.Module, env *jni.Env, activity uintptr, req rbxuri.Request) bool {
	if req.Empty() || env == nil {
		return false
	}
	webURL := req.WebLoginURI()
	baseHandled := callProtocolLaunch(mod, env, activity, "com/roblox/universalapp/linking/JNIBaseUrlProtocol", baseURLInitSym, baseURLColdStartSym, req.AndroidDeepLink)
	webHandled := callProtocolLaunch(mod, env, activity, "com/roblox/universalapp/linking/JNIWebLoginProtocol", webLoginInitSym, webLoginColdStartSym, webURL)
	logging.Logger(logging.CatRuntime).Info("cold-start protocol launch", "request", req.Summary(), "redeemed", req.TicketRedeemed, "base", baseHandled, "weblogin", webHandled)
	return baseHandled || webHandled
}

func callProtocolLaunch(mod *loader.Module, env *jni.Env, activity uintptr, class, initSym, launchSym, uri string) bool {
	if uri == "" {
		return false
	}
	cls := env.FindClass(class)
	if cls == 0 {
		return false
	}
	protocol := env.AllocObject(cls)
	callRobloxJNI(mod, env.Raw(), protocol, initSym, activity)
	result := callRobloxJNI(mod, env.Raw(), protocol, launchSym, env.NewStringUTF(uri))
	return result != 0
}

func startWebsiteGame(mod *loader.Module, env *jni.Env, gl, activity, platform, device, surface uintptr, req rbxuri.Request) {
	if req.PlaceID == 0 || env == nil {
		return
	}
	params := makeStartGameParams(env, activity, platform, device, surface, req)
	logging.Logger(logging.CatRuntime).Info("starting website experience", "request", req.Summary())
	callRobloxJNI(mod, env.Raw(), gl, startGameSym, params)
}

func makeStartGameParams(env *jni.Env, activity, platform, device, surface uintptr, req rbxuri.Request) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/StartGameParams"))
	origin := "Deeplink"
	if req.Scheme == "roblox-player" || req.Scheme == "https" {
		origin = "Website"
	}
	for k, v := range map[string]any{
		"surface": surface, "platformParams": platform, "deviceParams": device,
		"placeId": req.PlaceID, "userId": req.UserID, "conversationId": int64(0),
		"referredByPlayerId": req.ReferredByPlayerID, "isUnder13": false, "joinRequestType": int32(0),
		"username": "", "accessCode": req.AccessCode, "callId": "", "eventId": "",
		"gameId": req.GameInstanceID, "gameIdToExclude": "", "gameJoinContext": "",
		"isoContext": "", "joinAttemptId": "", "joinAttemptOrigin": origin,
		"launchData": req.LaunchData, "linkCode": req.LinkCode, "referralPage": req.ReferralPage,
		"reservedServerAccessCode": req.ReservedServerCode, "vrContext": activity,
	} {
		env.PutField(p, k, v)
	}
	return p
}

func appStarterPlace(req rbxuri.Request) string {
	if req.PlaceID == 0 {
		return ""
	}
	return strconv.FormatInt(req.PlaceID, 10)
}

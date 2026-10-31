import Foundation
import Combine
import AVFoundation
import Speech

/// Audio callbacks run off the main actor; changing recognition segments must
/// not race the input tap. Audio itself is never written to disk.
private final class PersonalAudioSink: @unchecked Sendable {
    private let lock = NSLock()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    func replace(_ request:SFSpeechAudioBufferRecognitionRequest?) { lock.lock();defer{lock.unlock()};self.request=request }
    func append(_ buffer:AVAudioPCMBuffer) { lock.lock();defer{lock.unlock()};request?.append(buffer) }
}

/// One recording owns one frozen target; partial and final recognition
/// segments accumulate until the user finishes, then send one complete text.
@MainActor
final class PersonalChatVoice: ObservableObject {
    @Published private(set) var isRecording = false
    @Published private(set) var isPreparing = false
    @Published private(set) var isFinishing = false
    @Published private(set) var transcript = ""
    private let engine = AVAudioEngine()
    private let sink = PersonalAudioSink()
    private var recognizer:SFSpeechRecognizer?
    private var request:SFSpeechAudioBufferRecognitionRequest?
    private var recognition:SFSpeechRecognitionTask?
    private var generation=UUID()
    private var segmentGeneration=UUID()
    private var segments:[String]=[]
    private var tapped=false
    private var finishTimeout:Task<Void,Never>?
    private var onFinal:((String)->Void)?
    private var onError:((Error)->Void)?

    func start(onFinal:@escaping(String)->Void,onError:@escaping(Error)->Void) async {
        cancel()
        let captured=generation
        isPreparing=true;transcript="";segments=[]
        self.onFinal=onFinal;self.onError=onError
        let authorization=await withCheckedContinuation { continuation in
            SFSpeechRecognizer.requestAuthorization { continuation.resume(returning:$0) }
        }
        guard generation==captured,!Task.isCancelled else{return}
        guard authorization == .authorized else{
            fail(APIError(summary:"语音识别不可用",detail:"NSSpeechRecognizer authorization: \(authorization.rawValue)"));return
        }
        let allowed=await AVAudioApplication.requestRecordPermission()
        guard generation==captured,!Task.isCancelled else{return}
        guard allowed else{fail(APIError(summary:"麦克风不可用",detail:"Microphone permission denied. Enable microphone access in iOS Settings."));return}
        let language=AppLanguage.currentLocale.language.languageCode?.identifier
        let locale=Locale(identifier:language=="zh" ? "zh-CN":"en-US")
        guard let recognizer=SFSpeechRecognizer(locale:locale),recognizer.isAvailable else{
            fail(APIError(summary:"语音识别不可用",detail:"SFSpeechRecognizer is unavailable for \(locale.identifier)"));return
        }
        self.recognizer=recognizer
        do {
            let audio=AVAudioSession.sharedInstance()
            try audio.setCategory(.record,mode:.measurement,options:.duckOthers)
            try audio.setActive(true,options:.notifyOthersOnDeactivation)
            isRecording=true
            beginSegment(recording:captured)
            let input=engine.inputNode
            let format=input.outputFormat(forBus:0)
            guard format.sampleRate>0,format.channelCount>0 else{throw APIError(summary:"麦克风不可用",detail:"Invalid audio input format: \(format)")}
            let sink=self.sink
            input.installTap(onBus:0,bufferSize:1024,format:format){buffer,_ in sink.append(buffer)}
            tapped=true
            engine.prepare();try engine.start();isPreparing=false
        }catch{fail(error)}
    }
    private func beginSegment(recording:UUID){
        guard let recognizer else{return}
        let segment=UUID();segmentGeneration=segment
        let request=SFSpeechAudioBufferRecognitionRequest()
        request.shouldReportPartialResults=true
        self.request=request;sink.replace(request)
        recognition=recognizer.recognitionTask(with:request){[weak self] result,error in
            let text=result?.bestTranscription.formattedString
            let final=result?.isFinal == true
            Task{@MainActor [weak self] in
                guard let self,self.generation==recording,self.segmentGeneration==segment else{return}
                if let text{self.transcript=(self.segments+[text]).joined(separator:"\n")}
                if let error{self.fail(error);return}
                if final{
                    if let text,!text.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty{self.segments.append(text)}
                    self.transcript=self.segments.joined(separator:"\n")
                    if self.isFinishing{self.complete()}
                    else if self.isRecording{self.beginSegment(recording:recording)}
                }
            }
        }
    }
    func finish(){
        guard isRecording,!isFinishing else{return}
        isRecording=false;isFinishing=true
        stopAudio();request?.endAudio();recognition?.finish()
        let captured=generation
        finishTimeout=Task{[weak self] in
            do{try await Task.sleep(for:.seconds(15))}catch{return}
            guard let self,self.generation==captured,self.isFinishing else{return}
            self.fail(APIError(summary:"语音识别失败",detail:"Speech recognizer did not produce a final transcript within 15 seconds after endAudio/finish. Partial transcript retained: \(self.transcript)"))
        }
    }
    func cancel(){
        generation=UUID();segmentGeneration=UUID();finishTimeout?.cancel();finishTimeout=nil
        sink.replace(nil);recognition?.cancel();recognition=nil;request=nil;recognizer=nil
        stopAudio();isPreparing=false;isRecording=false;isFinishing=false
        onFinal=nil;onError=nil
    }
    private func stopAudio(){
        engine.stop()
        if tapped{engine.inputNode.removeTap(onBus:0);tapped=false}
        do{try AVAudioSession.sharedInstance().setActive(false,options:.notifyOthersOnDeactivation)}
        catch{onError?(error)}
    }
    private func complete(){
        let text=segments.joined(separator:"\n").trimmingCharacters(in:.whitespacesAndNewlines)
        guard !text.isEmpty else{fail(APIError(summary:"没有识别到文字",detail:"Speech recognizer returned an empty final transcript."));return}
        let callback=onFinal;cancel();callback?(text)
    }
    private func fail(_ error:Error){let callback=onError;cancel();callback?(error)}
}

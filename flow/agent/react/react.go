/*
 * Copyright 2024 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package react 实现了ReAct（Reasoning and Acting）代理模式
// 这是一种能够处理用户消息、调用工具并根据工具响应继续执行的AI代理
package react

import (
	"context"
	"io"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/schema"
)

// state 定义了ReAct代理的内部状态
type state struct {
	// Messages 存储代理处理过程中的所有消息历史
	Messages                 []*schema.Message
	// ReturnDirectlyToolCallID 存储需要直接返回结果的工具调用ID
	ReturnDirectlyToolCallID string
}

// 计算图中的节点键名常量
const (
	// nodeKeyTools 工具节点的键名
	nodeKeyTools = "tools"
	// nodeKeyModel 模型节点的键名
	nodeKeyModel = "chat"
)

// MessageModifier 在模型被调用前修改输入消息的函数类型
// 可用于添加系统提示或其他消息处理
type MessageModifier func(ctx context.Context, input []*schema.Message) []*schema.Message

// AgentConfig is the config for ReAct agent.
// AgentConfig 是ReAct代理的配置结构
type AgentConfig struct {
	// ToolCallingModel is the chat model to be used for handling user messages with tool calling capability.
	// This is the recommended model field to use.
	// ToolCallingModel 是用于处理带有工具调用能力的用户消息的聊天模型
	// 这是推荐使用的模型字段
	ToolCallingModel model.ToolCallingChatModel

	// Deprecated: Use ToolCallingModel instead.
	// 已弃用: 请使用ToolCallingModel替代
	Model model.ChatModel

	// ToolsConfig is the config for tools node.
	// ToolsConfig 是工具节点的配置
	ToolsConfig compose.ToolsNodeConfig

	// MessageModifier.
	// modify the input messages before the model is called, it's useful when you want to add some system prompt or other messages.
	// MessageModifier 在模型被调用前修改输入消息
	// 当你想添加系统提示或其他消息时非常有用
	MessageModifier MessageModifier

	// MaxStep.
	// default 12 of steps in pregel (node num + 10).
	// MaxStep 最大执行步数
	// 默认值为12步 (pregel中的节点数 + 10)
	MaxStep int `json:"max_step"`

	// Tools that will make agent return directly when the tool is called.
	// When multiple tools are called and more than one tool is in the return directly list, only the first one will be returned.
	// 当调用这些工具时，代理将直接返回结果
	// 当多个工具被调用且多于一个工具在直接返回列表中时，只有第一个工具的结果会被返回
	ToolReturnDirectly map[string]struct{}

	// StreamOutputHandler is a function to determine whether the model's streaming output contains tool calls.
	// Different models have different ways of outputting tool calls in streaming mode:
	// - Some models (like OpenAI) output tool calls directly
	// - Others (like Claude) output text first, then tool calls
	// This handler allows custom logic to check for tool calls in the stream.
	// It should return:
	// - true if the output contains tool calls and agent should continue processing
	// - false if no tool calls and agent should stop
	// Note: This field only needs to be configured when using streaming mode
	// Note: The handler MUST close the modelOutput stream before returning
	// Optional. By default, it checks if the first chunk contains tool calls.
	// Note: The default implementation does not work well with Claude, which typically outputs tool calls after text content.
	// Note: If your ChatModel doesn't output tool calls first, you can try adding prompts to constrain the model from generating extra text during the tool call.
	// StreamToolCallChecker 是用于判断模型的流式输出是否包含工具调用的函数
	// 不同模型在流式模式下输出工具调用的方式不同:
	// - 有些模型（如OpenAI）直接输出工具调用
	// - 其他模型（如Claude）先输出文本，然后才是工具调用
	// 这个处理器允许自定义逻辑来检查流中的工具调用
	// 它应该返回:
	// - true 如果输出包含工具调用且代理应继续处理
	// - false 如果没有工具调用且代理应停止
	// 注意: 该字段只需要在使用流式模式时配置
	// 注意: 处理器必须在返回前关闭 modelOutput 流
	// 可选。默认情况下，它检查第一个块是否包含工具调用。
	// 注意: 默认实现对Claude等模型不太有效，因为这些模型通常在文本内容后才输出工具调用。
	// 注意: 如果你的ChatModel不先输出工具调用，可以尝试添加提示来约束模型在工具调用期间不生成额外文本。
	StreamToolCallChecker func(ctx context.Context, modelOutput *schema.StreamReader[*schema.Message]) (bool, error)
}

// Deprecated: This approach of adding persona involves unnecessary slice copying overhead.
// Instead, directly include the persona message in the input messages when calling Generate or Stream.
//
// NewPersonaModifier add the system prompt as persona before the model is called.
// example:
//
//	persona := "You are an expert in golang."
//	config := AgentConfig{
//		Model: model,
//		MessageModifier: NewPersonaModifier(persona),
//	}
//	agent, err := NewAgent(ctx, config)
//	if err != nil {return}
//	msg, err := agent.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "how to build agent with eino"}})
//	if err != nil {return}
//	println(msg.Content)
// 已弃用: 这种添加人设的方法涉及不必要的切片复制开销。
// 替代方法是在调用Generate或Stream时直接在输入消息中包含人设消息。
//
// NewPersonaModifier 在模型被调用前添加系统提示作为人设。
// 示例:
//
//	persona := "你是一个Golang专家。"
//	config := AgentConfig{
//		Model: model,
//		MessageModifier: NewPersonaModifier(persona),
//	}
//	agent, err := NewAgent(ctx, config)
//	if err != nil {return}
//	msg, err := agent.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "如何使用eino构建代理"}})
//	if err != nil {return}
//	println(msg.Content)
func NewPersonaModifier(persona string) MessageModifier {
	// 返回一个消息修改器函数，该函数在输入消息前添加系统提示
	return func(ctx context.Context, input []*schema.Message) []*schema.Message {
		// 创建一个新的消息切片，容量为原始输入加上一个系统消息
		res := make([]*schema.Message, 0, len(input)+1)

		// 先添加系统消息（人设）
		res = append(res, schema.SystemMessage(persona))
		// 然后添加原始输入消息
		res = append(res, input...)
		return res
	}
}

// firstChunkStreamToolCallChecker 检查流式输出的第一个非空块是否包含工具调用
// 这是默认的StreamToolCallChecker实现，主要用于OpenAI等在流式输出开始就包含工具调用的模型
func firstChunkStreamToolCallChecker(_ context.Context, sr *schema.StreamReader[*schema.Message]) (bool, error) {
	// 确保在函数返回前关闭流
	defer sr.Close()

	for {
		// 接收流中的下一个消息
		msg, err := sr.Recv()
		// 如果到达流的结尾，返回没有工具调用
		if err == io.EOF {
			return false, nil
		}
		// 如果出现其他错误，返回错误
		if err != nil {
			return false, err
		}

		// 如果消息包含工具调用，返回 true
		if len(msg.ToolCalls) > 0 {
			return true, nil
		}

		// 跳过前面的空块
		if len(msg.Content) == 0 { // skip empty chunks at the front
			continue
		}

		// 如果收到非空内容块且没有工具调用，则返回 false
		return false, nil
	}
}

// 计算图中使用的常量名称
const (
	// GraphName 是ReAct代理计算图的名称
	GraphName     = "ReActAgent"
	// ModelNodeName 是模型节点的名称
	ModelNodeName = "ChatModel"
	// ToolsNodeName 是工具节点的名称
	ToolsNodeName = "Tools"
)

// Agent is the ReAct agent.
// ReAct agent is a simple agent that handles user messages with a chat model and tools.
// ReAct will call the chat model, if the message contains tool calls, it will call the tools.
// if the tool is configured to return directly, ReAct will return directly.
// otherwise, ReAct will continue to call the chat model until the message contains no tool calls.
// e.g.
//
//	agent, err := ReAct.NewAgent(ctx, &react.AgentConfig{})
//	if err != nil {...}
//	msg, err := agent.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "how to build agent with eino"}})
//	if err != nil {...}
//	println(msg.Content)
// Agent 是 ReAct 代理的实现。
// ReAct 代理是一个简单的代理，使用聊天模型和工具处理用户消息。
// ReAct 将调用聊天模型，如果消息包含工具调用，它将调用这些工具。
// 如果工具配置为直接返回，ReAct 将直接返回结果。
// 否则，ReAct 将继续调用聊天模型，直到消息不再包含工具调用。
// 例如：
//
//	agent, err := ReAct.NewAgent(ctx, &react.AgentConfig{})
//	if err != nil {...}
//	msg, err := agent.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "如何使用eino构建代理"}})
//	if err != nil {...}
//	println(msg.Content)
type Agent struct {
	// runnable 是可执行的计算图实例
	runnable         compose.Runnable[[]*schema.Message, *schema.Message]
	// graph 是底层的计算图
	graph            *compose.Graph[[]*schema.Message, *schema.Message]
	// graphAddNodeOpts 是将该图添加到其他图时使用的选项
	graphAddNodeOpts []compose.GraphAddNodeOpt
}

// 使用sync.Once确保状态类型只注册一次
var registerStateOnce sync.Once

// NewAgent creates a ReAct agent that feeds tool response into next round of Chat Model generation.
//
// IMPORTANT!! For models that don't output tool calls in the first streaming chunk (e.g. Claude)
// the default StreamToolCallChecker may not work properly since it only checks the first chunk for tool calls.
// In such cases, you need to implement a custom StreamToolCallChecker that can properly detect tool calls.
// NewAgent 创建一个ReAct代理，该代理将工具响应输入到下一轮聊天模型生成中。
//
// 重要！！对于不在第一个流式块中输出工具调用的模型（如Claude）
// 默认的StreamToolCallChecker可能无法正常工作，因为它只检查第一个块是否包含工具调用。
// 在这种情况下，你需要实现自定义的StreamToolCallChecker以正确检测工具调用。
func NewAgent(ctx context.Context, config *AgentConfig) (_ *Agent, err error) {
	// 声明必要的变量
	var (
		chatModel       model.BaseChatModel       // 聊天模型
		toolsNode       *compose.ToolsNode        // 工具节点
		toolInfos       []*schema.ToolInfo        // 工具信息列表
		toolCallChecker = config.StreamToolCallChecker // 工具调用检查器
		messageModifier = config.MessageModifier       // 消息修改器
	)

	// 确保状态类型只注册一次
	registerStateOnce.Do(func() {
		err = compose.RegisterSerializableType[state]("_eino_react_state")
	})
	if err != nil {
		return
	}

	// 如果没有提供工具调用检查器，使用默认的实现
	if toolCallChecker == nil {
		toolCallChecker = firstChunkStreamToolCallChecker
	}

	// 生成工具信息列表
	if toolInfos, err = genToolInfos(ctx, config.ToolsConfig); err != nil {
		return nil, err
	}

	// 创建带有工具的聊天模型
	if chatModel, err = agent.ChatModelWithTools(config.Model, config.ToolCallingModel, toolInfos); err != nil {
		return nil, err
	}

	// 创建工具节点
	if toolsNode, err = compose.NewToolNode(ctx, &config.ToolsConfig); err != nil {
		return nil, err
	}

	// 创建新的计算图，并设置本地状态生成函数
	graph := compose.NewGraph[[]*schema.Message, *schema.Message](compose.WithGenLocalState(func(ctx context.Context) *state {
		return &state{Messages: make([]*schema.Message, 0, config.MaxStep+1)}
	}))

	// 定义模型节点的预处理函数，用于处理输入消息并应用消息修改器
	modelPreHandle := func(ctx context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
		// 将输入消息添加到状态的消息历史中
		state.Messages = append(state.Messages, input...)

		// 如果没有消息修改器，直接返回当前消息历史
		if messageModifier == nil {
			return state.Messages, nil
		}

		// 创建消息历史的副本，以避免修改原始消息
		modifiedInput := make([]*schema.Message, len(state.Messages))
		copy(modifiedInput, state.Messages)
		// 应用消息修改器并返回结果
		return messageModifier(ctx, modifiedInput), nil
	}

	// 向计算图添加聊天模型节点
	if err = graph.AddChatModelNode(nodeKeyModel, chatModel, compose.WithStatePreHandler(modelPreHandle), compose.WithNodeName(ModelNodeName)); err != nil {
		return nil, err
	}

	// 添加从起始节点到模型节点的边
	if err = graph.AddEdge(compose.START, nodeKeyModel); err != nil {
		return nil, err
	}

	// 定义工具节点的预处理函数
	toolsNodePreHandle := func(ctx context.Context, input *schema.Message, state *state) (*schema.Message, error) {
		// 如果输入为空，返回最后一条消息（用于重新运行中断恢复）
		if input == nil {
			return state.Messages[len(state.Messages)-1], nil // used for rerun interrupt resume
		}
		// 将模型输出添加到消息历史
		state.Messages = append(state.Messages, input)
		// 检查是否有需要直接返回的工具调用
		state.ReturnDirectlyToolCallID = getReturnDirectlyToolCallID(input, config.ToolReturnDirectly)
		return input, nil
	}
	// 向计算图添加工具节点
	if err = graph.AddToolsNode(nodeKeyTools, toolsNode, compose.WithStatePreHandler(toolsNodePreHandle), compose.WithNodeName(ToolsNodeName)); err != nil {
		return nil, err
	}

	// 定义模型节点后的分支条件，用于决定是继续处理工具调用还是结束
	modelPostBranchCondition := func(_ context.Context, sr *schema.StreamReader[*schema.Message]) (endNode string, err error) {
		// 使用工具调用检查器检查模型输出是否包含工具调用
		if isToolCall, err := toolCallChecker(ctx, sr); err != nil {
			return "", err
		} else if isToolCall {
			// 如果包含工具调用，跳转到工具节点
			return nodeKeyTools, nil
		}
		// 如果不包含工具调用，结束执行
		return compose.END, nil
	}

	// 添加模型节点后的分支，可以跳转到工具节点或结束
	if err = graph.AddBranch(nodeKeyModel, compose.NewStreamGraphBranch(modelPostBranchCondition, map[string]bool{nodeKeyTools: true, compose.END: true})); err != nil {
		return nil, err
	}

	// 如果配置了直接返回的工具，构建直接返回的逻辑
	if len(config.ToolReturnDirectly) > 0 {
		if err = buildReturnDirectly(graph); err != nil {
			return nil, err
		}
	} else {
		// 否则，工具节点执行完后返回模型节点继续处理
		if err = graph.AddEdge(nodeKeyTools, nodeKeyModel); err != nil {
			return nil, err
		}
	}

	// 设置计算图编译选项
	compileOpts := []compose.GraphCompileOption{compose.WithMaxRunSteps(config.MaxStep), compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithGraphName(GraphName)}
	// 编译计算图，生成可执行实例
	runnable, err := graph.Compile(ctx, compileOpts...)
	if err != nil {
		return nil, err
	}

	// 创建并返回Agent实例
	return &Agent{
		runnable:         runnable,
		graph:            graph,
		graphAddNodeOpts: []compose.GraphAddNodeOpt{compose.WithGraphCompileOptions(compileOpts...)},
	}, nil
}

// buildReturnDirectly 构建直接返回工具调用结果的逻辑
// 当工具调用被配置为直接返回时，该函数将在计算图中添加必要的节点和边
func buildReturnDirectly(graph *compose.Graph[[]*schema.Message, *schema.Message]) (err error) {
	// 定义直接返回函数，用于从消息流中提取直接返回的工具调用结果
	directReturn := func(ctx context.Context, msgs *schema.StreamReader[[]*schema.Message]) (*schema.StreamReader[*schema.Message], error) {
		// 使用StreamReaderWithConvert将消息数组流转换为单个消息流
		return schema.StreamReaderWithConvert(msgs, func(msgs []*schema.Message) (*schema.Message, error) {
			var msg *schema.Message
			// 处理状态，查找匹配的工具调用ID
			err = compose.ProcessState[*state](ctx, func(_ context.Context, state *state) error {
				for i := range msgs {
					// 如果找到匹配的工具调用ID，则返回该消息
					if msgs[i] != nil && msgs[i].ToolCallID == state.ReturnDirectlyToolCallID {
						msg = msgs[i]
						return nil
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			// 如果没有找到匹配的消息，返回错误
			if msg == nil {
				return nil, schema.ErrNoValue
			}
			return msg, nil
		}), nil
	}

	// 直接返回节点的键名
	nodeKeyDirectReturn := "direct_return"
	// 将直接返回函数添加为计算图中的Lambda节点
	if err = graph.AddLambdaNode(nodeKeyDirectReturn, compose.TransformableLambda(directReturn)); err != nil {
		return err
	}

	// this branch checks if the tool called should return directly. It either leads to END or back to ChatModel
	// 这个分支检查被调用的工具是否应该直接返回。它要么导向END，要么回到ChatModel
	err = graph.AddBranch(nodeKeyTools, compose.NewStreamGraphBranch(func(ctx context.Context, msgsStream *schema.StreamReader[[]*schema.Message]) (endNode string, err error) {
		// 关闭消息流，因为我们只需要检查状态
		msgsStream.Close()

		// 处理状态，决定下一个节点
		err = compose.ProcessState[*state](ctx, func(_ context.Context, state *state) error {
			// 如果有直接返回的工具调用ID，跳转到直接返回节点
			if len(state.ReturnDirectlyToolCallID) > 0 {
				endNode = nodeKeyDirectReturn
			} else {
				// 否则跳转回模型节点继续处理
				endNode = nodeKeyModel
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		return endNode, nil
	}, map[string]bool{nodeKeyModel: true, nodeKeyDirectReturn: true}))
	if err != nil {
		return err
	}

	// 添加从直接返回节点到结束节点的边
	return graph.AddEdge(nodeKeyDirectReturn, compose.END)
}

// genToolInfos 生成工具信息列表，用于提供给模型
// 该函数遍历配置中的所有工具，获取它们的信息
func genToolInfos(ctx context.Context, config compose.ToolsNodeConfig) ([]*schema.ToolInfo, error) {
	// 创建工具信息列表，初始容量为工具数量
	toolInfos := make([]*schema.ToolInfo, 0, len(config.Tools))
	// 遍历所有工具
	for _, t := range config.Tools {
		// 获取工具信息
		tl, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}

		// 添加到列表中
		toolInfos = append(toolInfos, tl)
	}

	return toolInfos, nil
}

// getReturnDirectlyToolCallID 获取需要直接返回的工具调用ID
// 如果消息中包含配置为直接返回的工具调用，返回该调用的ID
func getReturnDirectlyToolCallID(input *schema.Message, toolReturnDirectly map[string]struct{}) string {
	// 如果没有配置直接返回的工具，返回空字符串
	if len(toolReturnDirectly) == 0 {
		return ""
	}

	// 遍历消息中的所有工具调用
	for _, toolCall := range input.ToolCalls {
		// 检查工具名称是否在直接返回列表中
		if _, ok := toolReturnDirectly[toolCall.Function.Name]; ok {
			// 返回工具调用ID
			return toolCall.ID
		}
	}

	// 如果没有找到匹配的工具调用，返回空字符串
	return ""
}

// Generate generates a response from the agent.
// Generate 生成代理的响应。
// 该方法接收用户输入消息，并返回一个完整的响应消息。
func (r *Agent) Generate(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (*schema.Message, error) {
	// 调用底层可执行实例的Invoke方法，并传入代理选项
	return r.runnable.Invoke(ctx, input, agent.GetComposeOptions(opts...)...)
}

// Stream calls the agent and returns a stream response.
// Stream 调用代理并返回流式响应。
// 该方法接收用户输入消息，并返回一个消息流，可用于实时获取代理的响应。
func (r *Agent) Stream(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (output *schema.StreamReader[*schema.Message], err error) {
	// 调用底层可执行实例的Stream方法，并传入代理选项
	return r.runnable.Stream(ctx, input, agent.GetComposeOptions(opts...)...)
}

// ExportGraph exports the underlying graph from Agent, along with the []compose.GraphAddNodeOpt to be used when adding this graph to another graph.
// ExportGraph 导出代理的底层计算图，以及将该图添加到另一个图时使用的选项。
// 该方法允许将ReAct代理的计算图集成到更复杂的计算图中。
func (r *Agent) ExportGraph() (compose.AnyGraph, []compose.GraphAddNodeOpt) {
	// 返回底层计算图和图添加选项
	return r.graph, r.graphAddNodeOpts
}
